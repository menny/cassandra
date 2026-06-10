package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/menny/cassandra/core/config"
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

func (m *mockLLM) GenerateContent(ctx context.Context, msgs []llm.Message, _ []llm.ToolDef, _ int) (*llm.Response, error) {
	// Honor ctx at entry so a cancelled context surfaces as ctx.Err() before
	// any scripted response is returned. This is what real provider SDKs do,
	// and it is the only way a ctx-forwarding test at the double's boundary
	// can observe the cancellation path.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
			snapshot[i].ProviderMetadata = maps.Clone(msg.ProviderMetadata)
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

func (m *mockLLM) GenerateStructuredContent(ctx context.Context, msgs []llm.Message, schema map[string]any, _ llm.StructuredConfig) (*llm.Response, error) {
	m.schemas = append(m.schemas, schema)
	// For testing, just treat it like GenerateContent but record the call.
	return m.GenerateContent(ctx, msgs, nil, 0)
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
	handlers map[string]func(context.Context, llm.ToolCall) (string, error)
}

func newMockDispatcher() *mockDispatcher {
	return &mockDispatcher{handlers: make(map[string]func(context.Context, llm.ToolCall) (string, error))}
}

func (d *mockDispatcher) ToTools() []llm.ToolDef { return nil }

func (d *mockDispatcher) HandleCall(ctx context.Context, tc llm.ToolCall) (string, error) {
	if fn, ok := d.handlers[tc.Name]; ok {
		return fn(ctx, tc)
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
	truncated            []int
	mcpStatuses          []mcpStatus
	reviewHeaders        []reviewHeaderInfo
}

type mcpStatus struct {
	name   string
	status string
	err    error
}

type reviewHeaderInfo struct {
	files      int
	guidelines string
	model      string
}

func (s *spyReporter) ReportIteration(iter int) {
	s.iterations = append(s.iterations, iter)
}

func (s *spyReporter) ReportToolCalls(tcs []llm.ToolCall) {
	s.toolCalls = append(s.toolCalls, tcs...)
}

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
func (s *spyReporter) ReportTruncated(maxTokens int) {
	s.truncated = append(s.truncated, maxTokens)
}

func (s *spyReporter) ReportMCPStatus(name string, status string, err error) {
	s.mcpStatuses = append(s.mcpStatuses, mcpStatus{name: name, status: status, err: err})
}

func (s *spyReporter) ReportReviewHeader(files int, guidelines string, model string) {
	s.reviewHeaders = append(s.reviewHeaders, reviewHeaderInfo{files: files, guidelines: guidelines, model: model})
}
func (s *spyReporter) ReportConfig(cfg *config.Config, targetDir string) {}
func (s *spyReporter) ReportFetchingDiff()                               {}
func (s *spyReporter) ReportFetchingCommits()                            {}
func (s *spyReporter) ReportNoChanges()                                  {}
func (s *spyReporter) ReportReview(result string) error                  { return nil }
func (s *spyReporter) ReportReviewWritten(file string)                   {}
func (s *spyReporter) ReportStructuredReviewWritten(file string)         {}
func (s *spyReporter) ReportMetricsWritten(file string)                  {}
func (s *spyReporter) ReportWarning(msg string, err error)               {}
func (s *spyReporter) ReportError(err error)                             {}

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
		d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "ok", nil }

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
		if len(spy.truncated) != 0 {
			t.Errorf("expected 0 truncations reported, got %v", spy.truncated)
		}
	})

	t.Run("truncation reporting", func(t *testing.T) {
		spy := &spyReporter{}
		lm := &mockLLM{responses: []*llm.Response{
			{Text: "partial...", FinishReason: llm.FinishReasonLength},
		}}
		d := newMockDispatcher()

		agent := NewAgent(lm, d, WithReporter(spy))
		maxTokens := 128
		_, err := agent.RunReview(context.Background(), "sys", "", "req", 5, maxTokens)
		if err != nil {
			t.Fatal(err)
		}

		if len(spy.truncated) != 1 || spy.truncated[0] != maxTokens {
			t.Errorf("expected truncation report with %d tokens, got %v", maxTokens, spy.truncated)
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
		d.handlers["tool1"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "res1", nil }
		d.handlers["tool2"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "res2", nil }

		agent := NewAgent(nil, d, WithStderr(io.Discard))
		tc1 := llm.ToolCall{ID: "id1", Name: "tool1"}
		tc2 := llm.ToolCall{ID: "id2", Name: "tool2"}

		msg := agent.executeToolCalls(context.Background(), []llm.ToolCall{tc1, tc2})

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
		d.handlers["bad"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "", errors.New("boom") }

		agent := NewAgent(nil, d, WithStderr(io.Discard))
		msg := agent.executeToolCalls(context.Background(), []llm.ToolCall{{ID: "id1", Name: "bad"}})

		if len(msg.ToolResults) != 1 {
			t.Fatal("expected 1 result")
		}
		if msg.ToolResults[0].Content != "error: boom" {
			t.Errorf("expected error string in content, got: %q", msg.ToolResults[0].Content)
		}
	})

	t.Run("context propagation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		d := newMockDispatcher()
		d.handlers["tool"] = func(ctx context.Context, _ llm.ToolCall) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "ok", nil
		}

		agent := NewAgent(nil, d, WithStderr(io.Discard))
		msg := agent.executeToolCalls(ctx, []llm.ToolCall{{ID: "id1", Name: "tool"}})

		if len(msg.ToolResults) != 1 {
			t.Fatal("expected 1 result")
		}
		if msg.ToolResults[0].Content != "error: context canceled" {
			t.Errorf("expected context canceled error, got: %q", msg.ToolResults[0].Content)
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
	d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return wantResult, nil }

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
	expectedContent := wantResult + fmt.Sprintf(budgetNoteGeneral, 1, 5, 4)
	if toolMsg.ToolResults[0].Content != expectedContent {
		t.Errorf("tool result content: got %q, want %q", toolMsg.ToolResults[0].Content, expectedContent)
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
	d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "content", nil }

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
	d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "x", nil }

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
	d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "content", nil }

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
	d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "content", nil }

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
	d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "x", nil }

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

func TestAgent_ExtractStructuredReview_TruncationReporting(t *testing.T) {
	spy := &spyReporter{}
	structuredJSON := `{"approval":{"approved":true,"rationale":"ok","action":"APPROVE"},"files_review":[]}`
	lm := &mockLLM{responses: []*llm.Response{
		{Text: structuredJSON, FinishReason: llm.FinishReasonLength},
	}}

	agent := NewAgent(lm, newMockDispatcher(), WithReporter(spy))
	maxTokens := 128
	_, err := agent.ExtractStructuredReview(context.Background(), "sys", "raw", llm.StructuredConfig{MaxTokens: maxTokens})
	if err != nil {
		t.Fatal(err)
	}

	if len(spy.truncated) != 1 || spy.truncated[0] != maxTokens {
		t.Errorf("expected truncation report with %d tokens, got %v", maxTokens, spy.truncated)
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

// TestMockLLM_GenerateStructuredContent_ForwardsCanceledContext verifies
// that the test double forwards ctx to GenerateContent rather than silently
// substituting context.Background() — the exact bug fixed in 7acedcd. This
// is the only test that anchors cancellation at the double's boundary;
// without it, ExtractStructuredReview's cancellation coverage would pass
// even after a regression in GenerateStructuredContent's ctx forwarding.
func TestMockLLM_GenerateStructuredContent_ForwardsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lm := &mockLLM{responses: []*llm.Response{textResponse("never returned")}}

	_, err := lm.GenerateStructuredContent(ctx, nil, nil, llm.StructuredConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if lm.callIdx != 0 {
		t.Errorf("expected 0 calls (mock should short-circuit on cancelled ctx), got %d", lm.callIdx)
	}
}

// TestExtractStructuredReview_RespectsContextCancellation verifies that a
// cancelled ctx flows through ExtractStructuredReview's soft-retry loop and
// surfaces as a wrapped context.Canceled. With mockLLM now honoring ctx at
// entry, the cancellation is observed on the first call and the loop never
// enters its second attempt.
func TestExtractStructuredReview_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	lm := &mockLLM{responses: []*llm.Response{textResponse("never returned")}}

	agent := newTestAgent(lm, newMockDispatcher())

	_, err := agent.ExtractStructuredReview(ctx, "sys", "raw review", llm.StructuredConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	// mockLLM short-circuits on cancelled ctx; the first call never records.
	if lm.callIdx != 0 {
		t.Errorf("expected 0 LLM calls (mock short-circuit on cancelled ctx), got %d", lm.callIdx)
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

func TestExecuteToolCalls_Parallel(t *testing.T) {
	dispatcher := newMockDispatcher()

	var entered sync.WaitGroup
	entered.Add(2)
	release := make(chan struct{})

	dispatcher.handlers["parallel_tool"] = func(ctx context.Context, tc llm.ToolCall) (string, error) {
		entered.Done()
		<-release
		return "done", nil
	}

	agent := newTestAgent(&mockLLM{}, dispatcher)
	toolCalls := []llm.ToolCall{
		{ID: "1", Name: "parallel_tool"},
		{ID: "2", Name: "parallel_tool"},
	}

	// Run in parallel and wait for both to enter.
	resChan := make(chan llm.Message, 1)
	go func() {
		resChan <- agent.executeToolCalls(context.Background(), toolCalls)
	}()

	// If they are parallel, both will call entered.Done() and wait on release.
	// We wait for both to enter here.
	entered.Wait()

	// Now release both.
	close(release)

	msg := <-resChan

	if len(msg.ToolResults) != 2 {
		t.Fatalf("expected 2 results, got %d", len(msg.ToolResults))
	}

	// Results should be in order regardless of execution order.
	if msg.ToolResults[0].ToolCallID != "1" || msg.ToolResults[1].ToolCallID != "2" {
		t.Errorf("results out of order: %+v", msg.ToolResults)
	}
}

func TestExecuteToolCalls_PanicRecovery(t *testing.T) {
	dispatcher := newMockDispatcher()
	dispatcher.handlers["panicking_tool"] = func(ctx context.Context, tc llm.ToolCall) (string, error) {
		panic("boom")
	}
	dispatcher.handlers["normal_tool"] = func(ctx context.Context, tc llm.ToolCall) (string, error) {
		return "ok", nil
	}

	agent := newTestAgent(&mockLLM{}, dispatcher)
	toolCalls := []llm.ToolCall{
		{ID: "1", Name: "panicking_tool"},
		{ID: "2", Name: "normal_tool"},
	}

	msg := agent.executeToolCalls(context.Background(), toolCalls)

	if len(msg.ToolResults) != 2 {
		t.Fatalf("expected 2 results, got %d", len(msg.ToolResults))
	}

	// Check that the normal tool finished normally
	if msg.ToolResults[1].Content != "ok" {
		t.Errorf("expected normal tool result 'ok', got %q", msg.ToolResults[1].Content)
	}

	// Check that the panicking tool result contains the panic message
	if !strings.Contains(msg.ToolResults[0].Content, "tool panicked: boom") {
		t.Errorf("expected panic error message, got %q", msg.ToolResults[0].Content)
	}
}

func TestRunReview_IterationBudgetNote(t *testing.T) {
	lm := &mockLLM{
		responses: []*llm.Response{
			toolCallsResponse(makeToolCall("tc1", "read_file", map[string]any{"file_path": "foo.go"})),
			toolCallsResponse(makeToolCall("tc2", "read_file", map[string]any{"file_path": "bar.go"})),
			toolCallsResponse(makeToolCall("tc3", "read_file", map[string]any{"file_path": "baz.go"})),
			textResponse("forced final review done"),
		},
	}
	d := newMockDispatcher()
	d.handlers["read_file"] = func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args map[string]any
		_ = tc.UnmarshalArguments(&args)
		return fmt.Sprintf("content of %s", args["file_path"]), nil
	}

	agent := newTestAgent(lm, d)
	got, err := agent.RunReview(context.Background(), "sys", "", "req", 3, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got != "forced final review done" {
		t.Errorf("got final review %q, expected %q", got, "forced final review done")
	}

	// We expect 4 LLM calls in total (3 loop turns + 1 forced final review).
	if len(lm.calls) != 4 {
		t.Fatalf("expected 4 LLM calls, got %d", len(lm.calls))
	}

	// lm.calls[1] is the second call (Turn 2). The last message in history should be a ToolResult message.
	history2 := lm.calls[1]
	lastMsg2 := history2[len(history2)-1]
	if lastMsg2.Role != llm.RoleTool {
		t.Fatalf("expected last message in Turn 2 history to be RoleTool, got %v", lastMsg2.Role)
	}
	if len(lastMsg2.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result in Turn 2, got %d", len(lastMsg2.ToolResults))
	}
	resultContent2 := lastMsg2.ToolResults[0].Content
	if !strings.Contains(resultContent2, "[SYSTEM NOTE] Iteration 1 of 3. Budget remaining: 2 turns.") {
		t.Errorf("expected Turn 2 ToolResult to contain Case A budget note, got:\n%s", resultContent2)
	}
	if !strings.Contains(resultContent2, "Please minimize iterations: only request further tool calls if needed") {
		t.Errorf("expected Turn 2 ToolResult to contain relaxed Case A warning, got:\n%s", resultContent2)
	}

	// lm.calls[2] is the third call (Turn 3). The last message in history should be the next ToolResult message.
	history3 := lm.calls[2]
	lastMsg3 := history3[len(history3)-1]
	if lastMsg3.Role != llm.RoleTool {
		t.Fatalf("expected last message in Turn 3 history to be RoleTool, got %v", lastMsg3.Role)
	}
	if len(lastMsg3.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result in Turn 3, got %d", len(lastMsg3.ToolResults))
	}
	resultContent3 := lastMsg3.ToolResults[0].Content
	if !strings.Contains(resultContent3, "[SYSTEM NOTE] Iteration 2 of 3. 1 more turn left.") {
		t.Errorf("expected Turn 3 ToolResult to contain Case B budget note, got:\n%s", resultContent3)
	}
	if !strings.Contains(resultContent3, "This is your last turn to call tools! In the next turn, you will be forced to finalize") {
		t.Errorf("expected Turn 3 ToolResult to contain Case B last-turn warning, got:\n%s", resultContent3)
	}

	// lm.calls[3] is the fourth call (forced final review).
	// The last message in history must be the RoleUser cap message, and the second-to-last
	// must be the RoleTool message for tc3 containing NO budget note at all (remaining == 0).
	history4 := lm.calls[3]
	if len(history4) < 2 {
		t.Fatalf("expected at least 2 messages in Turn 4 history, got %d", len(history4))
	}

	capMsg := history4[len(history4)-1]
	if capMsg.Role != llm.RoleUser {
		t.Errorf("expected last message in Turn 4 history to be RoleUser capMsg, got role %v", capMsg.Role)
	}
	if !strings.Contains(capMsg.Text, "[SYSTEM] The maximum number of tool-call iterations (3) has been reached.") {
		t.Errorf("expected last message to contain cap message, got %q", capMsg.Text)
	}

	lastToolMsg := history4[len(history4)-2]
	if lastToolMsg.Role != llm.RoleTool {
		t.Fatalf("expected second-to-last message in Turn 4 history to be RoleTool, got %v", lastToolMsg.Role)
	}
	if len(lastToolMsg.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result in lastToolMsg, got %d", len(lastToolMsg.ToolResults))
	}
	resultContent4 := lastToolMsg.ToolResults[0].Content
	// Since remaining was 0, it should be exactly the raw output "content of baz.go" with NO [SYSTEM NOTE].
	if resultContent4 != "content of baz.go" {
		t.Errorf("expected Turn 4 ToolResult to be exactly raw content, got %q", resultContent4)
	}
}
