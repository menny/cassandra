package tools

import (
	"context"
	"testing"
)

func TestSubagentContext(t *testing.T) {
	type traceKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, "trace"))
	defer cancel()
	if got := SubagentFromContext(ctx); got != "" {
		t.Fatalf("main identity = %q", got)
	}
	scoped := WithSubagent(ctx, "Security")
	if got := SubagentFromContext(scoped); got != "Security" {
		t.Fatalf("subagent identity = %q", got)
	}
	if scoped.Value(traceKey{}) != "trace" {
		t.Fatal("parent context value was lost")
	}
	cancel()
	if scoped.Err() != context.Canceled {
		t.Fatalf("cancellation not propagated: %v", scoped.Err())
	}
}
