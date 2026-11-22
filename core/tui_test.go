package core

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestTUIReporter_PostReviewMethods(t *testing.T) {
	var stderr bytes.Buffer
	r := NewTuiReporter(&stderr, &stderr, nil)

	t.Run("ReportPostReviewUserQuery formats user query box", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewUserQuery("How does auth work?")
		output := stripANSI(stderr.String())
		require.Contains(t, output, "User:")
		require.Contains(t, output, "How does auth work?")
	})

	t.Run("ReportPostReviewReply renders reply markdown", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewReply("Auth uses JWT.")
		require.Contains(t, stripANSI(stderr.String()), "Auth uses JWT.")
	})
}

func TestTUIModel_StageReportsPreserveCompletionOrder(t *testing.T) {
	m := &tuiModel{}
	m.Update(iterationMsg{iter: 1, stage: "Subagent: Security"})
	m.Update(iterationMsg{iter: 1, stage: "Subagent: PR summary"})
	m.Update(stageReviewMsg{title: "PR summary", result: "Adds JWT auth."})
	m.Update(stageReviewMsg{title: "Security", result: "Check token expiry."})
	m.Update(iterationMsg{iter: 1, stage: "AI Code Review"})
	m.Update(stageReviewMsg{title: "Security", result: "Additional security report."})

	require.Len(t, m.stageReviews, 3, "repeated titles must append rather than overwrite")
	content := stripANSI(m.renderContent())
	for _, text := range []string{"📋 PR summary:", "Adds JWT auth.", "📋 Security:", "Check token expiry.", "Additional security report."} {
		require.Contains(t, content, text)
	}
	require.Less(t, strings.Index(content, "Adds JWT auth."), strings.Index(content, "Check token expiry."))
	require.Less(t, strings.Index(content, "Check token expiry."), strings.Index(content, "[AI Code Review - Iteration 1]"))
	require.Less(t, strings.Index(content, "[AI Code Review - Iteration 1]"), strings.Index(content, "Additional security report."))
	require.Equal(t, "Subagent: Security", m.iterations[0].stage)
}

func TestTUIModel_StageReportWithoutIterations(t *testing.T) {
	m := &tuiModel{}
	m.Update(stageReviewMsg{title: "Standalone report", result: "Summary content."})
	content := stripANSI(m.renderContent())
	require.Contains(t, content, "📋 Standalone report:")
	require.Contains(t, content, "Summary content.")
}

func TestTUIModel_ConcurrentStageToolProgress(t *testing.T) {
	m := &tuiModel{}
	m.Update(iterationMsg{iter: 1, stage: "Subagent: Security"})
	m.Update(iterationMsg{iter: 1, stage: "Subagent: Summary"})
	securityCall := llm.ToolCall{ID: "security/1", Name: "read_file"}
	summaryCall := llm.ToolCall{ID: "summary/1", Name: "read_file"}
	m.Update(toolCallsMsg{stage: "Subagent: Security", iter: 1, tcs: []llm.ToolCall{securityCall}})
	m.Update(toolCallsMsg{stage: "Subagent: Summary", iter: 1, tcs: []llm.ToolCall{summaryCall}})
	require.Len(t, m.iterations, 2)
	require.Equal(t, securityCall.ID, m.iterations[0].toolCalls[0].id)
	require.Equal(t, summaryCall.ID, m.iterations[1].toolCalls[0].id)

	m.Update(toolStatusMsg{id: securityCall.ID, name: securityCall.Name, status: "started"})
	require.Equal(t, "started", m.iterations[0].toolCalls[0].status)
	require.Equal(t, "queued", m.iterations[1].toolCalls[0].status)
	failure := errors.New("read failed")
	m.Update(toolStatusMsg{id: securityCall.ID, name: securityCall.Name, status: "failed", err: failure})
	require.Equal(t, "failed", m.iterations[0].toolCalls[0].status)
	require.ErrorIs(t, m.iterations[0].toolCalls[0].err, failure)

	m.Update(toolStatusMsg{id: "missing", name: "read_file", status: "completed"})
	require.Equal(t, "queued", m.iterations[1].toolCalls[0].status, "unknown ID must not fall back to name")
	m.Update(llmStatusMsg{stage: "Subagent: Security", status: "Finalizing", llmWaiting: true})
	require.Equal(t, "Finalizing", m.iterations[0].llmStatus)
	require.Equal(t, "Executing tool calls...", m.iterations[1].llmStatus)

	m.Update(toolCallsMsg{stage: "missing", iter: 1, tcs: []llm.ToolCall{{ID: "unknown"}}})
	require.Equal(t, summaryCall.ID, m.iterations[1].toolCalls[0].id)
}

func TestTUIModel_LegacyToolProgress(t *testing.T) {
	m := &tuiModel{}
	m.Update(iterationMsg{iter: 1})
	m.Update(toolCallsMsg{tcs: []llm.ToolCall{{Name: "read_file"}}})
	m.Update(toolStatusMsg{name: "read_file", status: "completed"})
	require.Equal(t, "completed", m.iterations[0].toolCalls[0].status)
}

func TestTUIModel_LLMStatusTargetsExactIteration(t *testing.T) {
	for _, stage := range []string{"", "Subagent: Security"} {
		t.Run(stage, func(t *testing.T) {
			m := &tuiModel{}
			m.Update(iterationMsg{stage: stage, iter: 1})
			m.Update(iterationMsg{stage: stage, iter: 2})
			m.Update(iterationMsg{stage: "Subagent: Summary", iter: 3})
			m.Update(llmStatusMsg{stage: stage, iter: 1, status: "Retrying", llmWaiting: true})
			require.Equal(t, "Retrying", m.iterations[0].llmStatus)
			require.Equal(t, "Waiting for LLM reply...", m.iterations[1].llmStatus)
			require.Equal(t, "Waiting for LLM reply...", m.iterations[2].llmStatus)

			m.Update(llmStatusMsg{stage: stage, iter: 99, status: "Unknown iteration"})
			m.Update(llmStatusMsg{stage: "missing", iter: 1, status: "Unknown stage"})
			require.NotContains(t, m.renderContent(), "Unknown")
		})
	}
}

func TestTUIModel_UsageSummaries(t *testing.T) {
	m := &tuiModel{}
	m.Update(usageSummaryMsg{stage: "Subagent: Security", total: llm.Usage{PromptTokens: 10, OutputTokens: 2}})
	m.Update(usageSummaryMsg{stage: "Subagent: Summary", total: llm.Usage{PromptTokens: 20, OutputTokens: 3}})
	m.Update(usageSummaryMsg{stage: "Subagent: Security", total: llm.Usage{PromptTokens: 30, OutputTokens: 4, CachedTokens: 5, ThinkingTokens: 6}})
	m.Update(usageSummaryMsg{total: llm.Usage{PromptTokens: 100, OutputTokens: 50}})

	require.Len(t, m.usageSummaries, 3, "cumulative snapshots replace a stage total instead of accumulating it")
	content := stripANSI(m.renderContent())
	require.Contains(t, content, "📈 [Subagent: Security] 35 in, 10 out (total)")
	require.Contains(t, content, "📈 [Subagent: Summary] 20 in, 3 out (total)")
	require.Contains(t, content, "📈 100 in, 50 out (total)")
	require.Less(t, strings.Index(content, "[Subagent: Security]"), strings.Index(content, "[Subagent: Summary]"))
	require.Equal(t, 3, strings.Count(content, "(total)"))
}

func TestTUIModel_ClearsSubagentSpinnerOnCompletion(t *testing.T) {
	t.Run("via zero-usage summary", func(t *testing.T) {
		m := &tuiModel{}
		m.Update(iterationMsg{iter: 1, stage: "Subagent: Security"})
		m.Update(llmStatusMsg{stage: "Subagent: Security", status: "Formulating final subagent: security...", llmWaiting: true})
		require.True(t, m.iterations[0].llmWaiting)

		m.Update(usageSummaryMsg{stage: "Subagent: Security", total: llm.Usage{}})
		require.False(t, m.iterations[0].llmWaiting)
		require.Equal(t, "Stage completed.", m.iterations[0].llmStatus)
	})

	t.Run("via stage review without overwriting normal completion", func(t *testing.T) {
		m := &tuiModel{}
		m.Update(iterationMsg{iter: 1, stage: "Subagent: Normal"})
		m.Update(toolCallsMsg{stage: "Subagent: Normal", iter: 1, tcs: nil})
		m.Update(usageSummaryMsg{stage: "Subagent: Normal", total: llm.Usage{PromptTokens: 5, OutputTokens: 2}})
		require.False(t, m.iterations[0].llmWaiting)
		require.Equal(t, "LLM turn completed.", m.iterations[0].llmStatus)

		m.Update(iterationMsg{iter: 1, stage: "Subagent: Capped"})
		m.Update(llmStatusMsg{stage: "Subagent: Capped", status: "Formulating final subagent: capped...", llmWaiting: true})
		m.Update(stageReviewMsg{title: "Capped", result: "Done."})
		require.False(t, m.iterations[1].llmWaiting)
		require.Equal(t, "Stage completed.", m.iterations[1].llmStatus)
	})
}

func TestTUIModel_UsageSummaryCounters(t *testing.T) {
	tests := []struct {
		name  string
		usage llm.Usage
		want  string
	}{
		{name: "zero"},
		{name: "unknown", usage: llm.UnknownUsage()},
		{name: "cache only", usage: llm.Usage{CachedTokens: 12}, want: "12 in, 0 out (total)"},
		{name: "thinking only", usage: llm.Usage{ThinkingTokens: 7}, want: "0 in, 7 out (total)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &tuiModel{}
			m.Update(usageSummaryMsg{total: tt.usage})
			if tt.want == "" {
				require.Empty(t, m.usageSummaries)
				require.Empty(t, m.renderContent())
			} else {
				require.Contains(t, stripANSI(m.renderContent()), tt.want)
			}
		})
	}
}

// tuiOutputCapture supports snapshots while Bubble Tea's renderer is active.
type tuiOutputCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *tuiOutputCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *tuiOutputCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestTUIReporter_StageDiagnosticsAndUsage(t *testing.T) {
	var stdout bytes.Buffer
	var stderr tuiOutputCapture
	r := NewTuiReporter(&stdout, &stderr, nil).(*tuiReporter)
	t.Cleanup(func() { require.NoError(t, r.Close()) })

	r.ReportStageIteration("Subagent: Security", 1)
	r.ReportStageIteration("Subagent: Security", 2)
	r.ReportStageIteration("Subagent: Summary", 1)
	r.ReportStageEmptyResponseRetry("Subagent: Security", 1, 3)
	r.ReportStageWarning("Subagent: Security", "Read failed", errors.New("permission denied"))
	r.ReportStageWarning("Subagent: Summary", "No context", nil)
	r.ReportStageCapReached("Subagent: Security", 5)
	r.ReportStageTruncated("Subagent: Summary", 1024)
	r.ReportStageUsageSummary("Subagent: Security", llm.Usage{PromptTokens: 10, OutputTokens: 2})
	r.ReportUsageSummary(llm.Usage{PromptTokens: 20, OutputTokens: 4})

	// No window-size message is sent for buffer output, so any usage line here
	// would be a direct reporter write rather than content drawn by the viewport.
	require.NotContains(t, stripANSI(stderr.String()), "in, 2 out (total)")
	require.NotContains(t, stripANSI(stderr.String()), "in, 4 out (total)")
	require.NoError(t, r.Close())

	require.Empty(t, stdout.String(), "progress must not leak into review output")
	require.Equal(t, "Retrying empty response (attempt 3)...", r.model.iterations[0].llmStatus)
	require.True(t, r.model.iterations[0].llmWaiting)
	require.Equal(t, "Waiting for LLM reply...", r.model.iterations[1].llmStatus)
	require.Equal(t, "Waiting for LLM reply...", r.model.iterations[2].llmStatus)
	output := stripANSI(stderr.String())
	for _, want := range []string{
		"[Subagent: Security] Read failed: permission denied",
		"[Subagent: Summary] No context",
		"[Subagent: Security] Reached maximum ReAct iterations (5). Forcing final review.",
		"[Subagent: Summary] LLM response truncated (hit max-tokens limit of 1024).",
		"📈 [Subagent: Security] 10 in, 2 out (total)",
		"📈 20 in, 4 out (total)",
	} {
		require.Contains(t, output, want)
	}
	require.Equal(t, 2, strings.Count(output, "(total)"))
}

func TestTUIReporter_NotifyUserWhileRunning(t *testing.T) {
	var stdout bytes.Buffer
	var stderr tuiOutputCapture
	r := NewTuiReporter(&stdout, &stderr, nil).(*tuiReporter)
	t.Cleanup(func() { require.NoError(t, r.Close()) })

	r.ReportStageIteration("Subagent: Security", 1)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.NotifyUser()
		}()
	}
	wg.Wait()
	require.NoError(t, r.Close())
	require.Contains(t, stderr.String(), "\a")
}

func TestIndentText(t *testing.T) {
	input := "line 1\nline 2\n\nline 3"
	got := indentText(input, 2)
	expected := "  line 1\n  line 2\n\n  line 3"
	require.Equal(t, expected, got)
}

func TestTUIModel_SubagentPresentation(t *testing.T) {
	m := &tuiModel{}
	m.Update(iterationMsg{iter: 1, stage: "AI Code Review"})
	m.Update(toolCallsMsg{stage: "AI Code Review", iter: 1, tcs: []llm.ToolCall{
		{ID: "main_1", Name: "run_subagent", Arguments: `{"title":"Trace auth","task":"secret task body"}`},
	}})
	m.Update(iterationMsg{iter: 1, stage: "Subagent: Trace auth #1"})
	m.Update(iterationMsg{iter: 2, stage: "Subagent: Trace auth #1"})
	m.Update(iterationMsg{iter: 1, stage: "Subagent: Trace auth #2"})

	content := stripANSI(m.renderContent())
	require.Contains(t, content, "🛰️ Subagent(Trace auth)")
	require.NotContains(t, content, "secret task body")
	require.NotContains(t, content, "run_subagent")
	require.Contains(t, content, "🛰️ [Subagent: Trace auth #1 - Iteration 2]")
	require.Contains(t, content, "🛰️ [Subagent: Trace auth #2 - Iteration 1]")
	require.Contains(t, content, "🔍 [AI Code Review - Iteration 1]")
	require.NotNil(t, m.findIteration("Subagent: Trace auth #2", 1))
	require.Equal(t, 1, m.findIteration("Subagent: Trace auth #2", 0).iter)
}
