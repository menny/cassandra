package core

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

type stagedEventReporter struct {
	spyReporter
	stage    string
	iter     int
	attempt  int
	usage    llm.Usage
	calls    []llm.ToolCall
	statuses []llm.ToolCall
}

func (s *stagedEventReporter) ReportStageToolCalls(stage string, iter int, calls []llm.ToolCall) {
	s.stage, s.iter, s.calls = stage, iter, calls
}

func (s *stagedEventReporter) ReportToolStatus(call llm.ToolCall, _ string, _ error) {
	s.statuses = append(s.statuses, call)
}

func (s *stagedEventReporter) ReportStageUsage(stage string, usage llm.Usage) {
	s.stage, s.usage = stage, usage
}

func (s *stagedEventReporter) ReportStageUsageSummary(stage string, usage llm.Usage) {
	s.stage, s.usage = stage, usage
}

func (s *stagedEventReporter) ReportStageEmptyResponseRetry(stage string, iter, attempt int) {
	s.stage, s.iter, s.attempt = stage, iter, attempt
}

func TestSubagentReporterScopedEvents(t *testing.T) {
	parent := &stagedEventReporter{}
	reporter := &subagentReporter{Reporter: parent, mu: &sync.Mutex{}, prefix: "subagent_0_"}
	for _, iter := range []int{1, 2} {
		reporter.ReportStageIteration("Security", iter)
		call := llm.ToolCall{ID: "same-id", Name: "read_file"}
		reporter.ReportToolCalls([]llm.ToolCall{call})
		reporter.ReportToolStatus(call, "completed", nil)
		require.Equal(t, parent.calls[0].ID, parent.statuses[len(parent.statuses)-1].ID)
		require.Equal(t, "Subagent: Security", parent.stage)
		require.Equal(t, iter, parent.iter)
		require.Equal(t, "same-id", call.ID)
	}
	require.NotEqual(t, parent.statuses[0].ID, parent.statuses[1].ID)
	reporter.ReportEmptyResponseRetry(3)
	require.Equal(t, "Subagent: Security", parent.stage)
	require.Equal(t, 2, parent.iter)
	require.Equal(t, 3, parent.attempt)
	usage := llm.Usage{PromptTokens: 10, OutputTokens: 5}
	reporter.ReportUsage(usage)
	require.Equal(t, usage, parent.usage)
	reporter.ReportUsageSummary(usage)
	require.Equal(t, usage, parent.usage)
}

func TestRunReviewToolCallCorrelation(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "review", true: "chat"}[chat], func(t *testing.T) {
			dispatcher := newMockDispatcher()
			runs := 0
			dispatcher.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) {
				runs++
				return "result", ctx.Err()
			}
			model := &mockLLM{responses: []*llm.Response{toolCallsResponse(
				llm.ToolCall{ID: "duplicate", Name: "read_file"},
				llm.ToolCall{ID: "duplicate", Name: "read_file"},
			)}}
			agent := newTestAgent(model, dispatcher)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			if chat {
				_, err = agent.RunChatFlight(ctx, "question", 1, 100)
			} else {
				_, err = agent.RunReview(ctx, "instructions", "", "request", 1, 100)
			}
			require.ErrorContains(t, err, "duplicate tool-call ID")
			require.Zero(t, runs)
			require.Empty(t, agent.History())
		})
	}
	calls := []llm.ToolCall{{Name: "read_file"}, {ID: "call_1_read_file", Name: "read_file"}}
	require.NoError(t, prepareToolCallIDs(calls))
	require.NotEmpty(t, calls[0].ID)
	require.NotEqual(t, calls[0].ID, calls[1].ID)
}

// Embedding only the base interface intentionally hides subagent policy support.
type unscopedDispatcher struct{ ToolDispatcher }

func TestPipelineRejectsUnscopedDispatcher(t *testing.T) {
	r, main, _ := newPipelineReviewer(t, &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{{Title: "Security", Prompt: "review", Required: true}}})
	r.Agent.registry = unscopedDispatcher{r.Agent.registry}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := r.Run(ctx, nil, "request")
	require.ErrorContains(t, err, "does not support subagent access policies")
	require.Empty(t, main.calls)
}

func TestSubagentReporterFallbackRetryIsAttributed(t *testing.T) {
	parent := &pipelineReporter{}
	reporter := &subagentReporter{Reporter: parent, mu: &sync.Mutex{}}
	reporter.ReportStageIteration("Summary", 1)
	reporter.ReportEmptyResponseRetry(2)
	require.Len(t, parent.warnings, 1)
	require.Contains(t, parent.warnings[0], "Subagent: Summary")
	reporter.ReportWarning("failed", errors.New("example"))
	require.Contains(t, parent.warnings[1], "Subagent: Summary")
}
