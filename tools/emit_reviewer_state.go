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
		Description: "Post a short progress update for the reviewer UI: what file or area you are examining and your current finding or status.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "A one-line factual status, e.g. 'checking callers of get_imports' or 'found an unused import'.",
				},
				"focus_area": map[string]any{
					"type":        "string",
					"description": "The file, function, or area this update is about (e.g., 'auth/token.go').",
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
