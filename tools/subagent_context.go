package tools

import "context"

type subagentContextKey struct{}

// WithSubagent identifies the calling subagent for user-facing tool interactions.
// Context cancellation and values are inherited unchanged from the parent.
func WithSubagent(ctx context.Context, title string) context.Context {
	return context.WithValue(ctx, subagentContextKey{}, title)
}

// SubagentFromContext returns the configured title, or empty for the main agent.
func SubagentFromContext(ctx context.Context) string {
	title, _ := ctx.Value(subagentContextKey{}).(string)
	return title
}
