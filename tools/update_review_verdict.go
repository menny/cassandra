package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/menny/cassandra/llm"
)

type updateReviewVerdictArgs struct {
	Verdict   string `json:"verdict"`
	Rationale string `json:"rationale"`
}

var (
	verdictStripPrefixRegex = regexp.MustCompile(`^[⏳✅❎❌💬✳️✳\s*_\x60#~-]+`)
	verdictStripSuffixRegex = regexp.MustCompile(`[.,:;!*_\x60#~]+$`)
)

func normalizeVerdict(raw string) (normalized string, emoji string, err error) {
	trimmed := strings.TrimSpace(raw)
	hasApproveMarker := strings.Contains(trimmed, "✅")
	hasRejectMarker := strings.Contains(trimmed, "❌") || strings.Contains(trimmed, "❎")
	hasCommentMarker := strings.Contains(trimmed, "💬") || strings.Contains(trimmed, "✳️") || strings.Contains(trimmed, "✳")

	clean := strings.ToLower(trimmed)
	clean = verdictStripPrefixRegex.ReplaceAllString(clean, "")
	clean = verdictStripSuffixRegex.ReplaceAllString(clean, "")
	clean = strings.TrimSpace(clean)

	isNegatedApproval := strings.HasPrefix(clean, "not ") ||
		strings.Contains(clean, " not ") ||
		strings.HasPrefix(clean, "un") ||
		strings.HasPrefix(clean, "dis") ||
		strings.HasPrefix(clean, "non-") ||
		strings.HasPrefix(clean, "no ") ||
		strings.Contains(clean, "cannot") ||
		strings.Contains(clean, "can't") ||
		strings.Contains(clean, "wont") ||
		strings.Contains(clean, "won't")

	if isNegatedApproval && (strings.Contains(clean, "approv") || strings.Contains(clean, "accept") || strings.Contains(clean, "lgtm")) {
		return "rejected", ReviewStateRejected, nil
	}

	switch {
	case !isNegatedApproval && !hasRejectMarker && (clean == "approved" || clean == "approve" || clean == "accept" || clean == "accepted" || clean == "lgtm" || (clean == "" && hasApproveMarker)):
		return "approved", ReviewStateApproved, nil
	case clean == "rejected" || clean == "reject" || clean == "request_changes" || clean == "request changes" || clean == "changes_requested" || clean == "changes requested" || (clean == "" && hasRejectMarker):
		return "rejected", ReviewStateRejected, nil
	case clean == "comment" || clean == "comments" || clean == "else" || clean == "neutral" || clean == "discussion" || clean == "advisory" || (clean == "" && hasCommentMarker):
		return "comment", ReviewStateElse, nil
	default:
		return "", "", fmt.Errorf("invalid verdict %q: must be one of 'approved', 'rejected', or 'comment'", raw)
	}
}

func registerUpdateReviewVerdict(r *Registry) {
	def := llm.ToolDef{
		Name: "update_review_verdict",
		Description: "Formally updates the overall review verdict when developer explanations, answers, or code modifications " +
			"resolve previous blocking issues or introduce new blockers. Updates the verdict to 'approved', 'rejected', or 'comment' " +
			"with a concise rationale, and updates the thread status.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{"approved", "rejected", "comment"},
					"description": "The updated verdict: 'approved' (no blockers remain), 'rejected' (blockers remain or introduced), or 'comment' (neutral/uncertain/advisory only).",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "A concise summary of why the verdict shifted (e.g., 'Author clarified that thread safety is handled by the parent pool').",
				},
			},
			"required": []string{"verdict", "rationale"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args updateReviewVerdictArgs) (string, error) {
		if strings.TrimSpace(args.Rationale) == "" {
			return "", errors.New("rationale is required explaining why the review verdict changed")
		}

		normalized, emoji, err := normalizeVerdict(args.Verdict)
		if err != nil {
			return "", err
		}

		if r.verdictListener != nil {
			if err := r.verdictListener(ctx, normalized, args.Rationale); err != nil {
				return "", fmt.Errorf("failed to notify verdict listener: %w", err)
			}
		}

		if IsT3Session() {
			if err := UpdateThreadTitlePrefix(ctx, emoji); err != nil {
				// Non-fatal: title update failure shouldn't abort the tool execution.
				return fmt.Sprintf("Updated verdict to %q (%s), but failed to update thread title prefix: %v", normalized, emoji, err), nil
			}
		}

		return fmt.Sprintf("Successfully updated review verdict to %q (%s). Rationale: %s", normalized, emoji, args.Rationale), nil
	})
}
