package core

// StructuredReview represents the final extracted code review in a machine-readable format.
type StructuredReview struct {
	RawFreeText        string       `json:"raw_free_text"`
	Approval           Approval     `json:"approval"`
	NoneSpecificReview string       `json:"none_specific_review,omitempty"`
	FilesReview        []FileReview `json:"files_review"`
}

// Approval represents the overall decision on the code changes.
type Approval struct {
	Approved  bool   `json:"approved"`
	Rationale string `json:"rationale"`
}

// FileReview represents feedback for a specific part of a file.
type FileReview struct {
	Path   string `json:"path"`
	Lines  string `json:"lines,omitempty"` // A single line ("42") or a single range ("10-25").
	Review string `json:"review"`
}

// StructuredReviewSchema is the JSON Schema representation of StructuredReview.
// This is used by LLM providers to enforce the output format.
var StructuredReviewSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"raw_free_text": map[string]any{
			"type":        "string",
			"description": "The complete, original markdown review from the Agent.",
		},
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
			},
			"required": []string{"approved", "rationale"},
		},
		"none_specific_review": map[string]any{
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
	"required": []string{"raw_free_text", "approval", "files_review"},
}
