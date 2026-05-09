package eval

import (
	"github.com/menny/cassandra/core"
)

// TestSuite represents a collection of evaluation cases.
type TestSuite struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Cases       []EvalCase `json:"cases"`
}

// EvalCase represents a single evaluation scenario.
type EvalCase struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Rubric is the specific criteria the Judge should use to score the review.
	Rubric string `json:"rubric"`

	// FixturePath is the relative path to the directory containing input.diff and base state.
	// This is resolved relative to the suite manifest file.
	FixturePath string `json:"fixture_path"`

	// BaseSource optionally overrides the default 'base.tar.gz' or 'base/' directory
	// resolution inside FixturePath.
	BaseSource string `json:"base_source,omitempty"`

	// DiffPath optionally overrides the default 'input.diff' inside FixturePath.
	DiffPath string `json:"diff_path,omitempty"`

	// Loaded data (not in manifest)
	Diff string `json:"-"`
}

// EvaluationResult is the structured output from the Judge.
type EvaluationResult struct {
	Score     int      `json:"score"`      // 1-5 Scale
	Rationale string   `json:"rationale"`  // Detailed explanation of the score
	Findings  []string `json:"findings"`   // Specific observations
	MetRubric bool     `json:"met_rubric"` // Whether the review satisfied the core rubric
}

// CaseResult captures the outcome of running a single EvalCase.
type CaseResult struct {
	CaseID   string              `json:"case_id"`
	CaseName string              `json:"case_name"`
	Subject  EvaluationResult    `json:"subject_result"`
	Metrics  core.SessionMetrics `json:"metrics"` // Subject metrics
	Error    string              `json:"error,omitempty"`
}

// EvaluationResultSchema is the JSON Schema for the Judge's output.
var EvaluationResultSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"score": map[string]any{
			"type":        "integer",
			"description": "A score from 1 to 5, where 5 is an excellent, accurate, and helpful review, and 1 is a poor or misleading review.",
			"minimum":     1,
			"maximum":     5,
		},
		"rationale": map[string]any{
			"type":        "string",
			"description": "A detailed explanation of why this score was given, referencing the rubric and the diff.",
		},
		"findings": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "string",
			},
			"description": "Specific strengths or weaknesses identified in the review.",
		},
		"met_rubric": map[string]any{
			"type":        "boolean",
			"description": "Whether the review successfully addressed the primary requirements of the rubric.",
		},
	},
	"required": []string{"score", "rationale", "findings", "met_rubric"},
}
