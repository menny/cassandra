package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
)

// Runner orchestrates the evaluation process.
type Runner struct {
	SubjectConfig *config.Config // Configuration for the agent under test
	Judge         llm.Model      // The model doing the evaluation
}

// RunCase executes a single evaluation case.
func (r *Runner) RunCase(ctx context.Context, c EvalCase) (*CaseResult, error) {
	// 1. Create Sandbox
	sandbox, err := NewSandbox(ctx, c.BaseSource, c.Diff)
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}
	defer sandbox.Cleanup()

	// 2. Setup Reviewer (Subject)
	reviewer, err := core.NewReviewer(ctx, r.SubjectConfig, sandbox.RootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to setup reviewer: %w", err)
	}
	defer reviewer.Close()

	// 3. Run Review (Subject)
	requestText := fmt.Sprintf("Review the following git diff for issues:\n\n%s", c.Diff)
	changedFiles := extractChangedFiles(c.Diff)

	review, err := reviewer.Run(ctx, changedFiles, requestText)
	if err != nil {
		return &CaseResult{
			CaseID:   c.ID,
			CaseName: c.Name,
			Error:    fmt.Sprintf("subject failed: %v", err),
		}, nil
	}

	// 4. Judge Review
	judgeResult, err := r.evaluate(ctx, c, review)
	if err != nil {
		return &CaseResult{
			CaseID:   c.ID,
			CaseName: c.Name,
			Error:    fmt.Sprintf("judge failed: %v", err),
		}, nil
	}

	return &CaseResult{
		CaseID:   c.ID,
		CaseName: c.Name,
		Subject:  *judgeResult,
		Metrics:  reviewer.Agent.GetMetrics(),
	}, nil
}

func (r *Runner) evaluate(ctx context.Context, c EvalCase, review string) (*EvaluationResult, error) {
	judgePrompt, err := prompts.GetLibraryPrompt("eval_judge")
	if err != nil {
		return nil, fmt.Errorf("failed to get judge prompt: %w", err)
	}

	userPrompt := fmt.Sprintf("RUBRIC:\n%s\n\nDIFF:\n%s\n\nREVIEW TO EVALUATE:\n%s", c.Rubric, c.Diff, review)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Text: judgePrompt},
		{Role: llm.RoleUser, Text: userPrompt},
	}

	resp, err := r.Judge.GenerateStructuredContent(ctx, messages, EvaluationResultSchema, llm.StructuredConfig{})
	if err != nil {
		return nil, err
	}

	var result EvaluationResult
	if err := json.Unmarshal([]byte(resp.Text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse judge result: %w\nRaw: %s", err, resp.Text)
	}

	return &result, nil
}

// LoadSuite loads a test suite from a JSON manifest file.
func LoadSuite(suitePath string) (*TestSuite, error) {
	data, err := os.ReadFile(suitePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read suite file: %w", err)
	}

	var suite TestSuite
	if err := json.Unmarshal(data, &suite); err != nil {
		return nil, fmt.Errorf("failed to parse suite file %s: %w", suitePath, err)
	}

	suiteDir := filepath.Dir(suitePath)

	for i := range suite.Cases {
		c := &suite.Cases[i]
		if c.FixturePath == "" {
			return nil, fmt.Errorf("case %s missing fixture_path", c.ID)
		}

		// Resolve physical directory containing files
		absFixturePath := c.FixturePath
		if !filepath.IsAbs(absFixturePath) {
			absFixturePath = filepath.Join(suiteDir, absFixturePath)
		}

		// 1. Load Diff
		diffName := c.DiffPath
		if diffName == "" {
			diffName = "input.diff"
		}
		diffContent, err := os.ReadFile(filepath.Join(absFixturePath, diffName))
		if err != nil {
			return nil, fmt.Errorf("failed to read diff for %s: %w", c.ID, err)
		}
		c.Diff = string(diffContent)

		// 2. Resolve Base State
		if c.BaseSource != "" {
			if !filepath.IsAbs(c.BaseSource) {
				c.BaseSource = filepath.Join(absFixturePath, c.BaseSource)
			}
		} else {
			// Auto-detect base.tar.gz or base/
			defaultTar := filepath.Join(absFixturePath, "base.tar.gz")
			if _, err := os.Stat(defaultTar); err == nil {
				c.BaseSource = defaultTar
			} else {
				defaultBase := filepath.Join(absFixturePath, "base")
				if info, err := os.Stat(defaultBase); err == nil && info.IsDir() {
					c.BaseSource = defaultBase
				}
			}
		}
	}

	return &suite, nil
}

func extractChangedFiles(diff string) []string {
	var files []string
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			file := strings.TrimPrefix(line, "+++ b/")
			files = append(files, file)
		}
	}
	return files
}
