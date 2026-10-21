package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/menny/cassandra/llm"
)

type updateReviewVerdictArgs struct {
	Verdict   string `json:"verdict"`
	Rationale string `json:"rationale"`
}

func normalizeVerdict(raw string) (normalized string, emoji string, err error) {
	clean := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(clean, "approv") || strings.Contains(raw, "✅"):
		return "approved", ReviewStateApproved, nil
	case strings.Contains(clean, "reject") || strings.Contains(raw, "❌") || strings.Contains(raw, "❎"):
		return "rejected", ReviewStateRejected, nil
	case strings.Contains(clean, "comment") || strings.Contains(clean, "else") || strings.Contains(raw, "✳️") || strings.Contains(raw, "💬"):
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

		var titleWarning string
		if IsT3Session() {
			if titleErr := UpdateThreadTitlePrefix(ctx, emoji); titleErr != nil {
				titleWarning = fmt.Sprintf(" (warning: failed to update thread title prefix: %v)", titleErr)
			}
		}

		if r.verdictListener != nil {
			if listenerErr := r.verdictListener(ctx, normalized, args.Rationale); listenerErr != nil {
				return "", fmt.Errorf("failed to notify verdict change listener: %w", listenerErr)
			}
		}

		if IsT3Session() {
			return fmt.Sprintf("Review verdict successfully updated to %q (%s)%s. Rationale: %s", normalized, emoji, titleWarning, args.Rationale), nil
		}
		return fmt.Sprintf("Review verdict successfully updated to %q. Rationale: %s", normalized, args.Rationale), nil
	})
}
