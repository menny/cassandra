package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
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
	VerdictComment  ReviewVerdict = "comment"
	VerdictElse     ReviewVerdict = VerdictComment
)

// DetermineVerdict extracts the ReviewVerdict from a review's markdown text.
// It inspects lines from the bottom of the review upwards to find the concluding verdict.
func DetermineVerdict(reviewText string) ReviewVerdict {
	lines := strings.Split(reviewText, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if v, ok := matchVerdictLine(line); ok {
			return v
		}
	}

	return VerdictComment
}

var (
	listPrefixRegex   = regexp.MustCompile(`^(\s*[-*+>]|\s*#+)\s*`)
	verdictLabelRegex = regexp.MustCompile(`(?i)^\*{0,2}(?:overall\s+|final\s+|review\s+)?(?:verdict|status)\*{0,2}\s*:\s*`)
	verdictDelims     = []string{"—", "–", "--", " - ", " : ", " | ", " ("}
)

func matchVerdictLine(line string) (ReviewVerdict, bool) {
	rawLine := strings.TrimSpace(line)
	if rawLine == "" {
		return VerdictComment, false
	}

	hasApproveEmoji := strings.Contains(rawLine, "✅")
	hasRejectEmoji := strings.Contains(rawLine, "❌") || strings.Contains(rawLine, "❎")
	hasCommentEmoji := strings.Contains(rawLine, "💬") || strings.Contains(rawLine, "✳️") || strings.Contains(rawLine, "✳")

	// Strip list item / blockquote / markdown header markers
	cleaned := strings.TrimSpace(listPrefixRegex.ReplaceAllString(rawLine, ""))
	// Strip leading verdict label like "**Verdict**:" or "Status:"
	cleaned = strings.TrimSpace(verdictLabelRegex.ReplaceAllString(cleaned, ""))

	// Delimiter isolation: split on separators that introduce rationale/explanation
	earliestIdx := -1
	for _, d := range verdictDelims {
		if idx := strings.Index(cleaned, d); idx != -1 {
			if earliestIdx == -1 || idx < earliestIdx {
				earliestIdx = idx
			}
		}
	}

	verdictPart := cleaned
	if earliestIdx != -1 {
		verdictPart = strings.TrimSpace(cleaned[:earliestIdx])
	}

	// Tokenize verdictPart into alphanumeric words
	var sb strings.Builder
	for _, r := range strings.ToLower(verdictPart) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(' ')
		}
	}
	words := strings.Fields(sb.String())

	// Fallback to emoji-only match when words are empty
	if len(words) == 0 {
		if hasApproveEmoji && !hasRejectEmoji {
			return VerdictApproved, true
		}
		if hasRejectEmoji {
			return VerdictRejected, true
		}
		if hasCommentEmoji {
			return VerdictComment, true
		}
		return VerdictComment, false
	}

	// Descriptive sentences or paragraphs that exceed verdict token length are not concluding verdicts.
	if len(words) > 3 {
		return VerdictComment, false
	}

	// Check for negation words within the isolated verdict tokens
	hasNegation := false
	for _, w := range words {
		if w == "not" || w == "unapproved" || w == "disapproved" || w == "never" ||
			w == "cannot" || w == "cant" || w == "wont" || w == "denied" {
			hasNegation = true
			break
		}
	}
	if !hasNegation && len(words) >= 2 && words[0] == "no" && (words[1] == "approval" || words[1] == "approve") {
		hasNegation = true
	}

	// Rejection checks (take precedence over approval if negations or reject markers exist)
	if hasRejectEmoji && !hasApproveEmoji && len(words) <= 2 {
		return VerdictRejected, true
	}
	if hasNegation && (containsWord(words, "approved") || containsWord(words, "approv") || containsWord(words, "accept") || containsWord(words, "lgtm")) {
		return VerdictRejected, true
	}
	if matchesWords(words, []string{"rejected"}) ||
		matchesWords(words, []string{"reject"}) ||
		matchesWords(words, []string{"request", "changes"}) ||
		matchesWords(words, []string{"changes", "requested"}) ||
		matchesWords(words, []string{"not", "approved"}) ||
		matchesWords(words, []string{"disapproved"}) ||
		matchesWords(words, []string{"unapproved"}) {
		return VerdictRejected, true
	}

	// Approval checks: strictly requiring positive approval tokens with no negations
	if !hasNegation && !hasRejectEmoji {
		if matchesWords(words, []string{"approved"}) ||
			matchesWords(words, []string{"approve"}) ||
			matchesWords(words, []string{"accepted"}) ||
			matchesWords(words, []string{"accept"}) ||
			matchesWords(words, []string{"lgtm"}) ||
			(hasApproveEmoji && (len(words) == 0 || (len(words) == 1 && (words[0] == "approved" || words[0] == "approve" || words[0] == "lgtm")))) {
			return VerdictApproved, true
		}
	}

	// Comment checks
	if hasCommentEmoji && len(words) <= 2 {
		return VerdictComment, true
	}
	if matchesWords(words, []string{"comment"}) ||
		matchesWords(words, []string{"comments"}) ||
		matchesWords(words, []string{"neutral"}) ||
		matchesWords(words, []string{"advisory"}) ||
		matchesWords(words, []string{"discussion"}) {
		return VerdictComment, true
	}

	return VerdictComment, false
}

func containsWord(words []string, target string) bool {
	for _, w := range words {
		if w == target || strings.HasPrefix(w, target) {
			return true
		}
	}
	return false
}

func matchesWords(words []string, target []string) bool {
	if len(words) != len(target) {
		return false
	}
	for i := range words {
		if words[i] != target[i] {
			return false
		}
	}
	return true
}
