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

// LoadCases loads all evaluation cases from the given directory.
func LoadCases(fixturesDir string) ([]EvalCase, error) {
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		return nil, err
	}

	var cases []EvalCase
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		caseDir := filepath.Join(fixturesDir, entry.Name())
		metadataPath := filepath.Join(caseDir, "metadata.json")
		b, err := os.ReadFile(metadataPath)
		if err != nil {
			continue // Skip directories without metadata.json
		}

		var c EvalCase
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", metadataPath, err)
		}

		// Fill in defaults and absolute paths
		if c.ID == "" {
			c.ID = entry.Name()
		}
		if c.DiffPath == "" {
			c.DiffPath = "input.diff"
		}

		diffContent, err := os.ReadFile(filepath.Join(caseDir, c.DiffPath))
		if err != nil {
			return nil, fmt.Errorf("failed to read diff for %s: %w", c.ID, err)
		}
		c.Diff = string(diffContent)

		if c.BaseSource != "" {
			c.BaseSource = filepath.Join(caseDir, c.BaseSource)
		} else {
			// Check for default 'base.tar.gz' first, then 'base' directory
			defaultTar := filepath.Join(caseDir, "base.tar.gz")
			if _, err := os.Stat(defaultTar); err == nil {
				c.BaseSource = defaultTar
			} else {
				defaultBase := filepath.Join(caseDir, "base")
				if info, err := os.Stat(defaultBase); err == nil && info.IsDir() {
					c.BaseSource = defaultBase
				}
			}
		}

		cases = append(cases, c)
	}

	return cases, nil
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
