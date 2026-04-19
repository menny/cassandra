package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/menny/cassandra/llm"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

// mockLLM records every GenerateContent call and returns scripted responses in order.
// If errs[i] is non-nil, that call returns the error instead of a response.
type mockLLM struct {
	responses []*llm.Response
	errs      []error          // optional per-call errors; nil entries mean "use responses"
	calls     [][]llm.Message  // captured message history per call, in order
	schemas   []map[string]any // captured schemas for structured calls
	callIdx   int
}

func (m *mockLLM) GenerateContent(_ context.Context, msgs []llm.Message, _ []llm.ToolDef, _ int) (*llm.Response, error) {
	snapshot := make([]llm.Message, len(msgs))
	for i, msg := range msgs {
		snapshot[i] = msg
		if msg.ToolCalls != nil {
			snapshot[i].ToolCalls = make([]llm.ToolCall, len(msg.ToolCalls))
			copy(snapshot[i].ToolCalls, msg.ToolCalls)
		}
		if msg.ToolResults != nil {
			snapshot[i].ToolResults = make([]llm.ToolResult, len(msg.ToolResults))
			copy(snapshot[i].ToolResults, msg.ToolResults)
		}
		if msg.ProviderMetadata != nil {
			snapshot[i].ProviderMetadata = make(map[string]any)
			for k, v := range msg.ProviderMetadata {
				snapshot[i].ProviderMetadata[k] = v
			}
		}
	}
	m.calls = append(m.calls, snapshot)

	idx := m.callIdx
	m.callIdx++
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx >= len(m.responses) {
		return nil, fmt.Errorf("mockLLM: no scripted response for call %d", idx+1)
	}
	return m.responses[idx], nil
}

func (m *mockLLM) GenerateStructuredContent(_ context.Context, msgs []llm.Message, schema map[string]any, _ llm.StructuredConfig) (*llm.Response, error) {
	m.schemas = append(m.schemas, schema)
	// For testing, just treat it like GenerateContent but record the call.
	return m.GenerateContent(context.Background(), msgs, nil, 0)
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
	handlers map[string]func(llm.ToolCall) (string, error)
}

func newMockDispatcher() *mockDispatcher {
	return &mockDispatcher{handlers: make(map[string]func(llm.ToolCall) (string, error))}
}

func (d *mockDispatcher) ToTools() []llm.ToolDef { return nil }

func (d *mockDispatcher) HandleCall(tc llm.ToolCall) (string, error) {
	if fn, ok := d.handlers[tc.Name]; ok {
		return fn(tc)
	}
	return "", fmt.Errorf("mockDispatcher: unknown tool %q", tc.Name)
}

// newTestAgent returns an Agent with stderr suppressed, suitable for unit tests.
func newTestAgent(model llm.Model, d ToolDispatcher) *Agent {
	return NewAgent(model, d, WithStderr(io.Discard))
}

// spyReporter records method calls for verification.
type spyReporter struct {
	iterations           []int
	toolCalls            []llm.ToolCall
	usage                []llm.Usage
	finalReviews         int
	extractions          int
	extractionRetries    []int
	emptyResponseRetries []int
	capsReached          []int
}

func (s *spyReporter) ReportIteration(iter int) {
	s.iterations = append(s.iterations, iter)
}
func (s *spyReporter) ReportToolCall(tc llm.ToolCall) { s.toolCalls = append(s.toolCalls, tc) }
func (s *spyReporter) ReportUsage(usage llm.Usage) {
	s.usage = append(s.usage, usage)
}
func (s *spyReporter) ReportUsageSummary(_ llm.Usage) {}
func (s *spyReporter) ReportFinalReview()             { s.finalReviews++ }
func (s *spyReporter) ReportExtraction()              { s.extractions++ }
func (s *spyReporter) ReportExtractionRetry(attempt int) {
	s.extractionRetries = append(s.extractionRetries, attempt)
}

func (s *spyReporter) ReportEmptyResponseRetry(attempt int) {
	s.emptyResponseRetries = append(s.emptyResponseRetries, attempt)
}
func (s *spyReporter) ReportCapReached(max int) { s.capsReached = append(s.capsReached, max) }

// ──────────────────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestAgent_Reporter(t *testing.T) {
	t.Run("happy-path reporting", func(t *testing.T) {
		spy := &spyReporter{}
		lm := &mockLLM{responses: []*llm.Response{
			toolCallsResponse(makeToolCall("tc1", "read_file", map[string]any{"file_path": "foo.go"})),
			textResponse("done"),
		}}
		d := newMockDispatcher()
		d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return "ok", nil }

		agent := NewAgent(lm, d, WithReporter(spy))
		_, err := agent.RunReview(context.Background(), "sys", "", "req", 5, 1024)
		if err != nil {
			t.Fatal(err)
		}

		if len(spy.iterations) != 2 {
			t.Errorf("expected 2 iterations reported, got %v", spy.iterations)
		}
		if len(spy.toolCalls) != 1 || spy.toolCalls[0].Name != "read_file" {
			t.Errorf("expected 1 tool call reported, got %v", spy.toolCalls)
		}
		if spy.finalReviews != 1 {
			t.Errorf("expected 1 final review report, got %d", spy.finalReviews)
		}
	})

	t.Run("no-tools edge-case", func(t *testing.T) {
		spy := &spyReporter{}
		lm := &mockLLM{responses: []*llm.Response{
			textResponse("direct answer"),
		}}
		agent := NewAgent(lm, newMockDispatcher(), WithReporter(spy))
		_, err := agent.RunReview(context.Background(), "sys", "", "req", 5, 1024)
		if err != nil {
			t.Fatal(err)
		}

		if len(spy.iterations) != 1 {
			t.Errorf("expected 1 iteration reported, got %v", spy.iterations)
		}
		if len(spy.toolCalls) != 0 {
			t.Errorf("expected no tool calls reported, got %v", spy.toolCalls)
		}
		if spy.finalReviews != 1 {
			t.Errorf("expected 1 final review report, got %d", spy.finalReviews)
		}
	})
}

func TestAgent_ExecuteToolCalls(t *testing.T) {
	t.Run("happy-path", func(t *testing.T) {
		d := newMockDispatcher()
		d.handlers["tool1"] = func(_ llm.ToolCall) (string, error) { return "res1", nil }
		d.handlers["tool2"] = func(_ llm.ToolCall) (string, error) { return "res2", nil }

		agent := NewAgent(nil, d, WithStderr(io.Discard))
		tc1 := llm.ToolCall{ID: "id1", Name: "tool1"}
		tc2 := llm.ToolCall{ID: "id2", Name: "tool2"}

		msg, err := agent.executeToolCalls([]llm.ToolCall{tc1, tc2})
		if err != nil {
			t.Fatal(err)
		}

		if msg.Role != llm.RoleTool {
			t.Errorf("expected RoleTool, got %v", msg.Role)
		}
		if len(msg.ToolResults) != 2 {
			t.Fatalf("expected 2 results, got %d", len(msg.ToolResults))
		}
		if msg.ToolResults[0].Content != "res1" || msg.ToolResults[1].Content != "res2" {
			t.Errorf("unexpected results: %+v", msg.ToolResults)
		}
	})

	t.Run("error-handling (individual tool failure)", func(t *testing.T) {
		d := newMockDispatcher()
		d.handlers["bad"] = func(_ llm.ToolCall) (string, error) { return "", fmt.Errorf("boom") }

		agent := NewAgent(nil, d, WithStderr(io.Discard))
		msg, err := agent.executeToolCalls([]llm.ToolCall{{ID: "id1", Name: "bad"}})
		if err != nil {
			t.Errorf("executeToolCalls should not return error on tool failure, got: %v", err)
		}

		if len(msg.ToolResults) != 1 {
			t.Fatal("expected 1 result")
		}
		if msg.ToolResults[0].Content != "error: boom" {
			t.Errorf("expected error string in content, got: %q", msg.ToolResults[0].Content)
		}
	})
}

// TestRunReview_DirectResponse verifies that when the LLM responds with plain
// text on the first call (no tool calls), RunReview returns that text immediately.
func TestRunReview_DirectResponse(t *testing.T) {
	lm := &mockLLM{responses: []*llm.Response{
		textResponse("looks good"),
	}}
	got, err := newTestAgent(lm, newMockDispatcher()).RunReview(context.Background(), "sys", "", "request", 5, 1024)
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
	d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return wantResult, nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "request", 5, 1024)
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
	d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return "content", nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "request", 5, 1024)
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
	d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return "x", nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "request", 2, 1024)
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

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "request", 5, 1024)
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

// TestRunReview_PreserveAssistantText verifies that if the LLM returns both text
// and tool calls, the text is preserved in the history.
func TestRunReview_PreserveAssistantText(t *testing.T) {
	const reasoning = "I need to read the file to be sure."
	lm := &mockLLM{responses: []*llm.Response{
		{
			Text: reasoning,
			ToolCalls: []llm.ToolCall{
				makeToolCall("tc1", "read_file", map[string]any{"file_path": "foo.go"}),
			},
		},
		textResponse("review done"),
	}}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return "content", nil }

	_, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "req", 5, 1024)
	if err != nil {
		t.Fatal(err)
	}

	// Second call: history should contain the assistant message WITH the reasoning text.
	msgs := lm.calls[1]
	assistantMsg := msgs[2]
	if assistantMsg.Role != llm.RoleAssistant {
		t.Errorf("expected RoleAssistant, got %v", assistantMsg.Role)
	}
	if assistantMsg.Text != reasoning {
		t.Errorf("assistant text: got %q, want %q", assistantMsg.Text, reasoning)
	}
	if len(assistantMsg.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(assistantMsg.ToolCalls))
	}
}

// TestRunReview_PreserveReasoningAndMetadata verifies that Reasoning and ProviderMetadata
// are correctly preserved in the history for subsequent turns.
func TestRunReview_PreserveReasoningAndMetadata(t *testing.T) {
	const reasoning = "I am thinking about this code."
	metadata := map[string]any{"thought_id": "123"}
	lm := &mockLLM{responses: []*llm.Response{
		{
			Reasoning:        reasoning,
			ProviderMetadata: metadata,
			ToolCalls: []llm.ToolCall{
				makeToolCall("tc1", "read_file", map[string]any{"file_path": "foo.go"}),
			},
		},
		textResponse("review done"),
	}}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return "content", nil }

	_, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "req", 5, 1024)
	if err != nil {
		t.Fatal(err)
	}

	// Second call: history should contain Reasoning and ProviderMetadata.
	msgs := lm.calls[1]
	assistantMsg := msgs[2]
	if assistantMsg.Reasoning != reasoning {
		t.Errorf("assistant reasoning: got %q, want %q", assistantMsg.Reasoning, reasoning)
	}
	if fmt.Sprintf("%v", assistantMsg.ProviderMetadata) != fmt.Sprintf("%v", metadata) {
		t.Errorf("assistant metadata: got %v, want %v", assistantMsg.ProviderMetadata, metadata)
	}
}

// TestRunReview_LowCapEnforcement verifies that maxIterations=1 is respected.
func TestRunReview_LowCapEnforcement(t *testing.T) {
	alwaysTool := toolCallsResponse(makeToolCall("tc", "read_file", map[string]any{"file_path": "f.go"}))
	lm := &mockLLM{responses: []*llm.Response{
		alwaysTool,                    // iteration 1
		textResponse("forced review"), // forced-final call
	}}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(_ llm.ToolCall) (string, error) { return "x", nil }

	got, err := newTestAgent(lm, d).RunReview(context.Background(), "sys", "", "request", 1, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "forced review" {
		t.Errorf("got %q, want %q", got, "forced review")
	}

	// 1 loop iteration + 1 forced-final call = 2 total LLM calls.
	if len(lm.calls) != 2 {
		t.Errorf("expected 2 LLM calls, got %d", len(lm.calls))
	}
}

// TestRunReview_CacheBreakpoint verifies that the stable system message always
// carries CacheBreakpoint:true so that providers supporting prefix caching
// (e.g. Anthropic) can cache it unconditionally. When dynamicSystem is
// non-empty, a second RoleSystem message is emitted without the flag.
func TestRunReview_CacheBreakpoint(t *testing.T) {
	t.Run("with dynamic content — two system messages, first has CacheBreakpoint", func(t *testing.T) {
		lm := &mockLLM{responses: []*llm.Response{textResponse("done")}}
		_, err := newTestAgent(lm, newMockDispatcher()).RunReview(context.Background(), "stable", "dynamic", "req", 5, 1024)
		if err != nil {
			t.Fatal(err)
		}

		msgs := lm.calls[0]
		if len(msgs) < 3 {
			t.Fatalf("expected at least 3 messages (2 system + 1 user), got %d", len(msgs))
		}
		if msgs[0].Role != llm.RoleSystem || msgs[0].Text != "stable" {
			t.Errorf("msgs[0]: got role=%v text=%q, want RoleSystem text=%q", msgs[0].Role, msgs[0].Text, "stable")
		}
		if !msgs[0].CacheBreakpoint {
			t.Error("msgs[0].CacheBreakpoint should be true for the stable prefix")
		}
		if msgs[1].Role != llm.RoleSystem || msgs[1].Text != "dynamic" {
			t.Errorf("msgs[1]: got role=%v text=%q, want RoleSystem text=%q", msgs[1].Role, msgs[1].Text, "dynamic")
		}
		if msgs[1].CacheBreakpoint {
			t.Error("msgs[1].CacheBreakpoint should be false for the dynamic suffix")
		}
	})

	t.Run("without dynamic content — single system message, CacheBreakpoint always true", func(t *testing.T) {
		lm := &mockLLM{responses: []*llm.Response{textResponse("done")}}
		_, err := newTestAgent(lm, newMockDispatcher()).RunReview(context.Background(), "stable", "", "req", 5, 1024)
		if err != nil {
			t.Fatal(err)
		}

		msgs := lm.calls[0]
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages (1 system + 1 user), got %d", len(msgs))
		}
		if msgs[0].Role != llm.RoleSystem || msgs[0].Text != "stable" {
			t.Errorf("msgs[0]: got role=%v text=%q, want RoleSystem text=%q", msgs[0].Role, msgs[0].Text, "stable")
		}
		if !msgs[0].CacheBreakpoint {
			t.Error("msgs[0].CacheBreakpoint should be true: stable prefix is always cacheable")
		}
	})
}

func TestAgent_ExtractStructuredReview(t *testing.T) {
	const rawReview = "LGTM! The code is clean.\n\nFile: main.go\nLine 10: good check."
	// Note: raw_free_text is NOT in the LLM output anymore.
	structuredJSON := `{
		"approval": {
			"approved": true,
			"rationale": "Code is clean"
		},
		"files_review": [
			{
				"path": "main.go",
				"lines": "10",
				"review": "good check."
			}
		]
	}`

	lm := &mockLLM{responses: []*llm.Response{
		textResponse(structuredJSON),
	}}

	agent := newTestAgent(lm, newMockDispatcher())
	got, err := agent.ExtractStructuredReview(context.Background(), "sys prompt", rawReview, llm.StructuredConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1. Verify LLM is NOT asked for raw_free_text in the schema.
	if len(lm.schemas) != 1 {
		t.Fatalf("expected 1 schema call, got %d", len(lm.schemas))
	}
	props := lm.schemas[0]["properties"].(map[string]any)
	if _, exists := props["raw_free_text"]; exists {
		t.Errorf("schema should NOT contain raw_free_text property")
	}

	// 2. Verify LLM did NOT provide RawFreeText.
	if got.RawFreeText != "" {
		t.Errorf("got RawFreeText %q, want empty", got.RawFreeText)
	}

	// 3. Verify final output includes it (simulating main.go assignment).
	got.RawFreeText = rawReview
	if got.RawFreeText != rawReview {
		t.Errorf("got RawFreeText %q, want %q after assignment", got.RawFreeText, rawReview)
	}

	if !got.Approval.Approved {
		t.Errorf("expected approved=true")
	}
	if len(got.FilesReview) != 1 || got.FilesReview[0].Path != "main.go" {
		t.Errorf("unexpected FilesReview: %+v", got.FilesReview)
	}
}

func TestCalculateMaxIterations(t *testing.T) {
	tests := []struct {
		name         string
		changedFiles int
		want         int
	}{
		{"zero files", 0, 1},
		{"negative files", -1, 1},
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

// TestExtractStructuredReview_RetryOnBadJSON verifies that ExtractStructuredReview
// retries when the LLM returns malformed JSON, and succeeds on the second attempt.
func TestExtractStructuredReview_RetryOnBadJSON(t *testing.T) {
	validJSON := `{"approval":{"approved":true,"rationale":"ok","action":"APPROVE"},"files_review":[]}`

	// First response: bad JSON. Second response: valid JSON.
	lm := &mockLLM{responses: []*llm.Response{
		textResponse("not-json"),
		textResponse(validJSON),
	}}

	spy := &spyReporter{}
	agent := NewAgent(lm, newMockDispatcher(), WithReporter(spy))

	got, err := agent.ExtractStructuredReview(context.Background(), "sys", "raw review", llm.StructuredConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Approval.Approved {
		t.Errorf("expected approved=true")
	}

	// Should have made 2 LLM calls.
	if lm.callIdx != 2 {
		t.Errorf("expected 2 LLM calls, got %d", lm.callIdx)
	}

	// The retry should have been reported once (for the 2nd attempt).
	if len(spy.extractionRetries) != 1 || spy.extractionRetries[0] != 2 {
		t.Errorf("expected extractionRetries=[2], got %v", spy.extractionRetries)
	}

	// ReportExtraction should have been called exactly once at the start.
	if spy.extractions != 1 {
		t.Errorf("expected 1 extraction report, got %d", spy.extractions)
	}
}

// TestExtractStructuredReview_ExhaustsRetries verifies that ExtractStructuredReview
// returns an error after exhausting all attempts.
func TestExtractStructuredReview_ExhaustsRetries(t *testing.T) {
	// All responses are bad JSON.
	lm := &mockLLM{responses: []*llm.Response{
		textResponse("bad1"),
		textResponse("bad2"),
		textResponse("bad3"),
	}}

	agent := newTestAgent(lm, newMockDispatcher())

	_, err := agent.ExtractStructuredReview(context.Background(), "sys", "raw review", llm.StructuredConfig{})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}

	// Should have made exactly extractionMaxAttempts calls.
	if lm.callIdx != extractionMaxAttempts {
		t.Errorf("expected %d LLM calls, got %d", extractionMaxAttempts, lm.callIdx)
	}
}

// TestExtractStructuredReview_LLMErrorReturnsImmediately verifies that a hard
// LLM error (non-nil err from GenerateStructuredContent) is returned immediately
// without any outer retry — the RetryingModel layer has already exhausted its
// own budget.
func TestExtractStructuredReview_LLMErrorReturnsImmediately(t *testing.T) {
	hardErr := errors.New("401 Unauthorized")

	lm := &mockLLM{
		errs: []error{hardErr},
	}

	agent := newTestAgent(lm, newMockDispatcher())

	_, err := agent.ExtractStructuredReview(context.Background(), "sys", "raw review", llm.StructuredConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Must stop after exactly one call — no outer retry on hard errors.
	if lm.callIdx != 1 {
		t.Errorf("expected 1 LLM call, got %d", lm.callIdx)
	}
}

// TestRunReview_EmptyResponseRetry verifies that when the LLM returns a
// successful but empty response (no text, no tool calls), the agent retries
// the call and eventually succeeds.
func TestRunReview_EmptyResponseRetry(t *testing.T) {
	lm := &mockLLM{responses: []*llm.Response{
		{Text: ""},                  // empty — should trigger retry
		textResponse("good review"), // succeeds on second attempt
	}}

	spy := &spyReporter{}
	agent := NewAgent(lm, newMockDispatcher(), WithReporter(spy))

	got, err := agent.RunReview(context.Background(), "sys", "", "request", 5, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "good review" {
		t.Errorf("got %q, want %q", got, "good review")
	}

	// 2 underlying calls — one empty, one good.
	if lm.callIdx != 2 {
		t.Errorf("expected 2 LLM calls, got %d", lm.callIdx)
	}

	// The retry should have been reported once.
	if len(spy.emptyResponseRetries) != 1 || spy.emptyResponseRetries[0] != 2 {
		t.Errorf("expected emptyResponseRetries=[2], got %v", spy.emptyResponseRetries)
	}
}

// TestRunReview_EmptyResponseExhausted verifies that when all attempts return
// empty content, RunReview fails with a descriptive error rather than silently
// returning an empty string.
func TestRunReview_EmptyResponseExhausted(t *testing.T) {
	// All responses are empty (no text, no tool calls).
	responses := make([]*llm.Response, emptyResponseMaxAttempts)
	for i := range responses {
		responses[i] = &llm.Response{Text: ""}
	}
	lm := &mockLLM{responses: responses}

	agent := newTestAgent(lm, newMockDispatcher())

	_, err := agent.RunReview(context.Background(), "sys", "", "request", 5, 1024)
	if err == nil {
		t.Fatal("expected error when empty-response retries are exhausted, got nil")
	}

	// At least emptyResponseMaxAttempts calls must have been made.
	if lm.callIdx < emptyResponseMaxAttempts {
		t.Errorf("expected at least %d LLM calls, got %d", emptyResponseMaxAttempts, lm.callIdx)
	}
}
