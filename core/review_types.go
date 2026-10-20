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

// SessionMetrics captures detailed usage and execution metrics for an agent session.
type SessionMetrics struct {
	Tokens     TokenMetrics    `json:"tokens"`
	Iterations int             `json:"iterations"`
	ToolCalls  ToolCallMetrics `json:"tool_calls"`
}

// Add accumulates other's token, iteration, and tool-call counts into m.
func (m *SessionMetrics) Add(other SessionMetrics) {
	m.Tokens.Input += other.Tokens.Input
	m.Tokens.Output += other.Tokens.Output
	m.Tokens.Thinking += other.Tokens.Thinking
	m.Tokens.Cached += other.Tokens.Cached
	m.Tokens.TotalInput += other.Tokens.TotalInput
	m.Tokens.TotalOutput += other.Tokens.TotalOutput
	m.Iterations += other.Iterations
	m.ToolCalls.Total += other.ToolCalls.Total
	if len(other.ToolCalls.ByTool) > 0 && m.ToolCalls.ByTool == nil {
		m.ToolCalls.ByTool = make(map[string]int, len(other.ToolCalls.ByTool))
	}
	for k, v := range other.ToolCalls.ByTool {
		m.ToolCalls.ByTool[k] += v
	}
}

// PhaseMetrics captures the metrics for a specific review phase.
type PhaseMetrics struct {
	Phase   string         `json:"phase"`
	Metrics SessionMetrics `json:"metrics"`
}

// ReviewMetrics captures the list of phase-specific metrics.
type ReviewMetrics struct {
	Phases []PhaseMetrics `json:"metrics"`
}

// TokenMetrics breaks down token consumption by category.
type TokenMetrics struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	Thinking    int `json:"thinking"`
	Cached      int `json:"cached"`
	TotalInput  int `json:"total_input"`
	TotalOutput int `json:"total_output"`
}

// ToolCallMetrics tracks how many times tools were invoked.
type ToolCallMetrics struct {
	Total  int            `json:"total"`
	ByTool map[string]int `json:"by_tool"`
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
		line, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid line format: %w", err)
		}
		return line, line, nil
	}

	if len(parts) == 2 {
		startLine, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid start line format: %w", err)
		}
		endLine, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid end line format: %w", err)
		}

		if startLine > endLine {
			return endLine, startLine, nil
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

// ReviewVerdict represents the high-level outcome of a code review.
type ReviewVerdict string

const (
	VerdictApproved ReviewVerdict = "approved"
	VerdictRejected ReviewVerdict = "rejected"
	VerdictElse     ReviewVerdict = "else"
)

// DetermineVerdict extracts the ReviewVerdict from a review's markdown text.
// It inspects lines from the bottom of the review upwards to find the concluding verdict.
func DetermineVerdict(reviewText string) ReviewVerdict {
	lines := strings.Split(reviewText, "\n")
	nonEmptyCount := 0
	for i := len(lines) - 1; i >= 0 && nonEmptyCount < 25; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		nonEmptyCount++

		// Explicit emoji markers
		if strings.Contains(line, "✅") && (strings.Contains(strings.ToLower(line), "approv") || strings.Contains(line, "Approved")) {
			return VerdictApproved
		}
		if (strings.Contains(line, "❌") || strings.Contains(line, "❎")) && (strings.Contains(strings.ToLower(line), "reject") || strings.Contains(line, "Rejected")) {
			return VerdictRejected
		}
		if strings.Contains(line, "💬") && (strings.Contains(strings.ToLower(line), "comment") || strings.Contains(line, "Comment")) {
			return VerdictElse
		}

		// Bullet point, heading, or bold without emoji (e.g. "- Approved", "**Approved**", "Verdict: Approved")
		clean := strings.Trim(line, "-*# \t`")
		cleanLower := strings.ToLower(clean)
		if strings.HasPrefix(cleanLower, "approved") || strings.HasPrefix(cleanLower, "verdict: approved") || strings.HasPrefix(cleanLower, "status: approved") {
			return VerdictApproved
		}
		if strings.HasPrefix(cleanLower, "rejected") || strings.HasPrefix(cleanLower, "verdict: rejected") || strings.HasPrefix(cleanLower, "status: rejected") {
			return VerdictRejected
		}
		if strings.HasPrefix(cleanLower, "comment") || strings.HasPrefix(cleanLower, "verdict: comment") || strings.HasPrefix(cleanLower, "status: comment") {
			return VerdictElse
		}
	}
	return VerdictElse
}
