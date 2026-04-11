package core

import (
	"fmt"
	"strconv"
	"strings"
)

// StructuredReview represents the final extracted code review in a machine-readable format.
type StructuredReview struct {
	RawFreeText       string       `json:"raw_free_text"`
	Approval          Approval     `json:"approval"`
	NonSpecificReview string       `json:"non_specific_review,omitempty"`
	FilesReview       []FileReview `json:"files_review"`
}

// Approval represents the overall decision on the code changes.
type Approval struct {
	Approved  bool   `json:"approved"`
	Rationale string `json:"rationale"`
	Action    string `json:"action,omitempty"` // The GitHub review action: "APPROVE", "REQUEST_CHANGES", "COMMENT".
}

// FileReview represents feedback for a specific part of a file.
type FileReview struct {
	Path   string `json:"path"`
	Lines  string `json:"lines,omitempty"` // A single line ("42") or a single range ("10-25").
	Review string `json:"review"`
}

// ParseLines parses the 'lines' string into individual line numbers.
// Returns startLine and endLine. For single lines, startLine == endLine.
func (fr *FileReview) ParseLines() (int, int, error) {
	if fr.Lines == "" {
		return 0, 0, nil
	}

	parts := strings.Split(fr.Lines, "-")
	if len(parts) == 1 {
		line, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid line format: %v", err)
		}
		return line, line, nil
	}

	if len(parts) == 2 {
		startLine, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid start line format: %v", err)
		}
		endLine, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid end line format: %v", err)
		}
		return startLine, endLine, nil
	}

	return 0, 0, fmt.Errorf("invalid lines format: %s", fr.Lines)
}

// StructuredReviewSchema is the JSON Schema representation of StructuredReview.
// This is used by LLM providers to enforce the output format.
// Note: raw_free_text is excluded from the schema to save tokens; it is
// populated manually by the caller.
var StructuredReviewSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"approval": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approved": map[string]any{
					"type":        "boolean",
					"description": "Whether the changes are approved for merging.",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "The high-level reasoning for the approval or rejection.",
				},
				"action": map[string]any{
					"type":        "string",
					"description": "The GitHub review action: 'APPROVE' if approved, 'REQUEST_CHANGES' if there are issues, or 'COMMENT' for neutral feedback.",
					"enum":        []string{"APPROVE", "REQUEST_CHANGES", "COMMENT"},
				},
			},
			"required": []string{"approved", "rationale", "action"},
		},
		"non_specific_review": map[string]any{
			"type":        "string",
			"description": "General review notes that are not tied to specific files or line ranges.",
		},
		"files_review": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The relative path to the file being reviewed.",
					},
					"lines": map[string]any{
						"type":        "string",
						"description": "The specific line number (e.g., '42') or range (e.g., '10-25') this review applies to.",
					},
					"review": map[string]any{
						"type":        "string",
						"description": "The detailed feedback for this specific chunk of code.",
					},
				},
				"required": []string{"path", "review"},
			},
		},
	},
	"required": []string{"approval", "files_review"},
}
