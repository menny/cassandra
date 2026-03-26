package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/menny/cassandra/llm"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

// mockLLM records every GenerateContent call and returns scripted responses in order.
type mockLLM struct {
	responses []*llm.Response
	calls     [][]llm.Message // captured message history per call, in order
	callIdx   int
}

func (m *mockLLM) GenerateContent(_ context.Context, msgs []llm.Message, _ []llm.ToolDef, _ int) (*llm.Response, error) {
	snapshot := make([]llm.Message, len(msgs))
	copy(snapshot, msgs)
	m.calls = append(m.calls, snapshot)

	if m.callIdx >= len(m.responses) {
		return nil, fmt.Errorf("mockLLM: no scripted response for call %d", m.callIdx+1)
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

// textResponse builds a Response with plain text and no tool calls.
func textResponse(content string) *llm.Response {
	return &llm.Response{Text: content}
}

// toolCallsResponse builds a Response whose single choice requests the given tool calls.
func toolCallsResponse(tcs ...llm.ToolCall) *llm.Response {
	return &llm.Response{ToolCalls: tcs}
}

// makeToolCall builds a ToolCall with JSON-encoded arguments.
func makeToolCall(id, name string, args map[string]any) llm.ToolCall {
	b, _ := json.Marshal(args)
	return llm.ToolCall{ID: id, Name: name, Arguments: string(b)}
}

// mockDispatcher is a minimal ToolDispatcher stub.
type mockDispatcher struct {
	handlers map[string]func(map[string]any) (string, error)
}

func newMockDispatcher() *mockDispatcher {
	return &mockDispatcher{handlers: make(map[string]func(map[string]any) (string, error))}
}

func (d *mockDispatcher) ToTools() []llm.ToolDef { return nil }

func (d *mockDispatcher) HandleCall(name string, args map[string]any) (string, error) {
	if fn, ok := d.handlers[name]; ok {
		return fn(args)
	}
	return "", fmt.Errorf("mockDispatcher: unknown tool %q", name)
}

// newTestAgent returns an Agent with stderr suppressed, suitable for unit tests.
func newTestAgent(model llm.Model, d ToolDispatcher) *Agent {
	return NewAgent(model, d, WithStderr(io.Discard))
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────────────────

// TestRunReview_DirectResponse verifies that when the LLM responds with plain
// text on the first call (no tool calls), RunReview returns that text immediately.
func TestRunReview_DirectResponse(t *testing.T) {
	lm := &mockLLM{responses: []*llm.Response{
		textResponse("looks good"),
	}}
	got, err := newTestAgent(lm, newMockDispatcher()).RunReview(context.Background(), "sys", "request", 5, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "looks good" {
		t.Errorf("got %q, want %q", got, "looks good")
	}
	if len(lm.calls) != 1 {
		t.Errorf("expected 1 LLM call, got %d", len(lm.calls))
	}
}

// TestRunReview_SingleToolCall verifies the one-tool-call happy path:
// iteration 1 → tool requested → iteration 2 → final text.
func TestRunReview_SingleToolCall(t *testing.T) {
	const wantResult = "file contents"
	lm := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(makeToolCall("tc1", "read_file", map[string]any{"file_path": "foo.go"})),
		textResponse("review done"),
	}}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(_ map[string]any) (string, error) { return wantResult, nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "request", 5, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "review done" {
		t.Errorf("got %q, want %q", got, "review done")
	}

	// Expect exactly 2 LLM calls.
	if len(lm.calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(lm.calls))
	}

	// Second call: [sys, human, ai+toolcall, tool-result] — four messages.
	msgs := lm.calls[1]
	if len(msgs) != 4 {
		t.Fatalf("second call: expected 4 messages, got %d", len(msgs))
	}

	// msgs[2] must be the assistant turn with ToolCalls.
	assistantMsg := msgs[2]
	if assistantMsg.Role != llm.RoleAssistant {
		t.Errorf("msgs[2] role: got %v, want RoleAssistant", assistantMsg.Role)
	}
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call in assistant msg, got %d", len(assistantMsg.ToolCalls))
	}

	// msgs[3] must be a RoleTool entry with a single result.
	toolMsg := msgs[3]
	if toolMsg.Role != llm.RoleTool {
		t.Errorf("msgs[3] role: got %v, want RoleTool", toolMsg.Role)
	}
	if len(toolMsg.ToolResults) != 1 {
		t.Fatalf("tool-result message: expected 1 result, got %d", len(toolMsg.ToolResults))
	}
	if toolMsg.ToolResults[0].Content != wantResult {
		t.Errorf("tool result content: got %q, want %q", toolMsg.ToolResults[0].Content, wantResult)
	}
}

// TestRunReview_MultipleToolCallsInOneTurn asserts that when the LLM requests
// two tools in a single turn, both responses are packed into ONE
// RoleTool message (not two separate messages).
// This is the regression test for the Error 400 bug: separate messages caused
// consecutive user-role turns that Gemini rejects.
func TestRunReview_MultipleToolCallsInOneTurn(t *testing.T) {
	lm := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(
			makeToolCall("tc1", "read_file", map[string]any{"file_path": "a.go"}),
			makeToolCall("tc2", "read_file", map[string]any{"file_path": "b.go"}),
		),
		textResponse("multi-tool review"),
	}}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(_ map[string]any) (string, error) { return "content", nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "request", 5, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "multi-tool review" {
		t.Errorf("got %q, want %q", got, "multi-tool review")
	}

	// Second call: [sys, human, ai+2toolcalls, ONE combined tool-result — 4 msgs].
	if len(lm.calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(lm.calls))
	}
	msgs := lm.calls[1]
	if len(msgs) != 4 {
		t.Errorf("second call: expected 4 messages, got %d", len(msgs))
	}
	toolMsg := msgs[3]
	if toolMsg.Role != llm.RoleTool {
		t.Errorf("msgs[3] role: got %v, want RoleTool", toolMsg.Role)
	}
	// Both results must be in this single message — not two separate messages.
	if len(toolMsg.ToolResults) != 2 {
		t.Errorf("expected 2 ToolResults in one message, got %d", len(toolMsg.ToolResults))
	}
}

// TestRunReview_CapReached verifies that when the LLM exhausts maxIterations
// without producing a text response, the agent injects the cap message and
// makes one final GenerateContent call.
func TestRunReview_CapReached(t *testing.T) {
	alwaysTool := toolCallsResponse(makeToolCall("tc", "read_file", map[string]any{"file_path": "f.go"}))
	lm := &mockLLM{responses: []*llm.Response{
		alwaysTool,                    // iteration 1
		alwaysTool,                    // iteration 2 (cap = 2)
		textResponse("forced review"), // forced-final call
	}}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(_ map[string]any) (string, error) { return "x", nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "request", 2, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "forced review" {
		t.Errorf("got %q, want %q", got, "forced review")
	}

	// 2 loop iterations + 1 forced-final call = 3 total LLM calls.
	if len(lm.calls) != 3 {
		t.Errorf("expected 3 LLM calls, got %d", len(lm.calls))
	}

	// The last call's final message must be the [SYSTEM] cap user turn.
	lastMsgs := lm.calls[2]
	last := lastMsgs[len(lastMsgs)-1]
	if last.Role != llm.RoleUser {
		t.Errorf("cap message role: got %v, want RoleUser", last.Role)
	}
	if last.Text == "" {
		t.Error("expected non-empty [SYSTEM] cap text in final user turn")
	}
}

// TestRunReview_ToolError verifies that when a tool returns an error the agent
// surfaces the error as the tool result text (so the LLM can reason about it)
// and continues the loop rather than aborting.
func TestRunReview_ToolError(t *testing.T) {
	lm := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(makeToolCall("tc", "bad_tool", nil)),
		textResponse("reviewed despite error"),
	}}
	// bad_tool is not registered → HandleCall will return an error.
	d := newMockDispatcher()

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "request", 5, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "reviewed despite error" {
		t.Errorf("got %q, want %q", got, "reviewed despite error")
	}

	// The error must have been forwarded as the tool result content, not a Go error.
	msgs := lm.calls[1]
	toolMsg := msgs[3]
	if toolMsg.ToolResults[0].Content == "" {
		t.Error("expected error text in tool result content, got empty string")
	}
}

func TestCalculateMaxIterations(t *testing.T) {
	tests := []struct {
		name         string
		changedFiles int
		want         int
	}{
		{"zero files", 0, AbsoluteMaxIter},
		{"negative files", -1, AbsoluteMaxIter},
		{"1 file", 1, 5},
		{"2 files", 2, 10},
		{"5 files", 5, 25},
		{"6 files (capped)", 6, 25},
		{"huge files (capped)", 100, 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateMaxIterations(tt.changedFiles); got != tt.want {
				t.Errorf("CalculateMaxIterations(%d) = %d, want %d", tt.changedFiles, got, tt.want)
			}
		})
	}
}
