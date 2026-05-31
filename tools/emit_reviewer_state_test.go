package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/menny/cassandra/llm"
)

func TestEmitReviewerState(t *testing.T) {
	r := NewRegistry()
	registerEmitReviewerState(r)

	t.Run("without focus_area", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := ContextWithStateWriter(context.Background(), &buf)

		args, err := json.Marshal(map[string]any{
			"message": "Analyzing repository structure to locate DB connection pooling",
		})
		if err != nil {
			t.Fatal(err)
		}

		result, err := r.HandleCall(ctx, llm.ToolCall{
			Name:      "emit_reviewer_state",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}

		expectedAck := `{"status": "state_recorded"}`
		if result != expectedAck {
			t.Errorf("expected result %q, got %q", expectedAck, result)
		}

		expectedOutput := "[reviewing] Analyzing repository structure to locate DB connection pooling\n"
		if buf.String() != expectedOutput {
			t.Errorf("expected printed output %q, got %q", expectedOutput, buf.String())
		}
	})

	t.Run("with focus_area", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := ContextWithStateWriter(context.Background(), &buf)

		args, err := json.Marshal(map[string]any{
			"message":    "Refactoring DB connection limit bounds check",
			"focus_area": "db/pool.go",
		})
		if err != nil {
			t.Fatal(err)
		}

		result, err := r.HandleCall(ctx, llm.ToolCall{
			Name:      "emit_reviewer_state",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}

		expectedAck := `{"status": "state_recorded"}`
		if result != expectedAck {
			t.Errorf("expected result %q, got %q", expectedAck, result)
		}

		expectedOutput := "[focus on 'db/pool.go'] Refactoring DB connection limit bounds check\n"
		if buf.String() != expectedOutput {
			t.Errorf("expected printed output %q, got %q", expectedOutput, buf.String())
		}
	})

	t.Run("default to os.Stderr if no writer provided", func(t *testing.T) {
		args, err := json.Marshal(map[string]any{
			"message": "Testing default writer fallback",
		})
		if err != nil {
			t.Fatal(err)
		}

		// Verify it does not error or panic
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "emit_reviewer_state",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}

		expectedAck := `{"status": "state_recorded"}`
		if result != expectedAck {
			t.Errorf("expected result %q, got %q", expectedAck, result)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		var buf bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		ctx = ContextWithStateWriter(ctx, &buf)

		args, err := json.Marshal(map[string]any{
			"message": "Should not write on canceled context",
		})
		if err != nil {
			t.Fatal(err)
		}

		_, err = r.HandleCall(ctx, llm.ToolCall{
			Name:      "emit_reviewer_state",
			Arguments: string(args),
		})
		if err == nil {
			t.Error("expected error due to canceled context, got nil")
		}
		if buf.Len() > 0 {
			t.Errorf("expected no output on canceled context, got %q", buf.String())
		}
	})
}
