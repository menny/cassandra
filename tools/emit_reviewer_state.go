package tools

import (
	"context"
	"fmt"
	"io"

	"github.com/menny/cassandra/llm"
)

type stateWriterKeyType struct{}

var stateWriterKey stateWriterKeyType

// ContextWithStateWriter returns a new context carrying the given state writer.
// This is retained for test assertions and potential downstream integrations
// where state is logged directly via context (rather than through reporter callbacks).
func ContextWithStateWriter(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, stateWriterKey, w)
}

// StateWriterFromContext extracts the state writer from the context, if present.
func StateWriterFromContext(ctx context.Context) io.Writer {
	if w, ok := ctx.Value(stateWriterKey).(io.Writer); ok {
		return w
	}
	return nil
}

type emitReviewerStateArgs struct {
	Message   string `json:"message"`
	FocusArea string `json:"focus_area,omitempty"`
}

func registerEmitReviewerState(r *Registry) {
	def := llm.ToolDef{
		Name:        "emit_reviewer_state",
		Description: "Use this tool to narrate your review process, formulate plans, explain why you are pivoting your search, or record intermediate findings before making a final decision. Do not use raw text for internal monologue; always use this tool.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Your detailed rationale, thought process, or current status.",
				},
				"focus_area": map[string]any{
					"type":        "string",
					"description": "The specific file, function, or domain you are currently evaluating (e.g., 'auth/token.go', 'DB connection pooling').",
				},
			},
			"required": []string{"message"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args emitReviewerStateArgs) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		writer := StateWriterFromContext(ctx)
		if writer != nil {
			var output string
			if args.FocusArea != "" {
				output = fmt.Sprintf("[focus on '%s'] %s\n", args.FocusArea, args.Message)
			} else {
				output = fmt.Sprintf("[reviewing] %s\n", args.Message)
			}
			_, _ = fmt.Fprint(writer, output)
		}

		return `{"status": "state_recorded"}`, nil
	})
}
