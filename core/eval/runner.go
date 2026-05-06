package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
)

// Runner orchestrates the evaluation process.
type Runner struct {
	Subject llm.Model // The model being evaluated
	Judge   llm.Model // The model doing the evaluation
}

// RunCase executes a single evaluation case.
func (r *Runner) RunCase(ctx context.Context, c EvalCase) (*CaseResult, error) {
	// 1. Create Sandbox
	sandbox, err := NewSandbox(ctx, c.BaseDir, c.Diff)
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}
	defer sandbox.Cleanup()

	// 2. Setup Agent (Subject)
	registry := tools.NewRegistry()
	tools.RegisterLocalTools(registry, sandbox.RootDir, nil) // No ignored lock files for now

	agent := core.NewAgent(r.Subject, registry)

	// Build the request text
	requestText := fmt.Sprintf("Review the following git diff for issues:\n\n%s", c.Diff)

	// Use general guidelines for now
	guidelines, err := prompts.GetLibraryPrompt("general")
	if err != nil {
		return nil, fmt.Errorf("failed to get general guidelines: %w", err)
	}

	// We don't have PR metadata or extra guidelines for eval cases yet
	stable, dynamic, _, err := prompts.BuildSystemPrompt(sandbox.RootDir, nil, guidelines, nil, "")
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	// 3. Run Review (Subject)
	review, err := agent.RunReview(ctx, stable, dynamic, requestText, 0, 0)
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
		Metrics:  agent.GetMetrics(),
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

		if c.BaseDir != "" {
			c.BaseDir = filepath.Join(caseDir, c.BaseDir)
		} else {
			// Check if a default 'base' directory exists
			defaultBase := filepath.Join(caseDir, "base")
			if info, err := os.Stat(defaultBase); err == nil && info.IsDir() {
				c.BaseDir = defaultBase
			}
		}

		cases = append(cases, c)
	}

	return cases, nil
}
