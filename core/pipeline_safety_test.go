package core

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
	"github.com/stretchr/testify/require"
)

// Main-agent tool completions can also arrive concurrently, outside the
// pipeline's reporter lock. Keep the embedded history-tracking spy race-free.
type pipelineSafetyReporter struct {
	pipelineFocusReporter
	statusMu      sync.Mutex
	cancelUpdates chan context.CancelFunc
}

func (s *pipelineSafetyReporter) ReportToolStatus(call llm.ToolCall, status string, err error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.pipelineFocusReporter.ReportToolStatus(call, status, err)
}

func (s *pipelineSafetyReporter) SetReviewCancel(cancel context.CancelFunc) {
	if s.cancelUpdates != nil {
		s.cancelUpdates <- cancel
	}
}

func pipelineSafetyToolNames(defs []llm.ToolDef) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	slices.Sort(names)
	return names
}

func pipelineSafetyResults(t *testing.T, model *mockLLM) map[string]llm.ToolResult {
	t.Helper()
	require.Len(t, model.calls, 2)
	results := make(map[string]llm.ToolResult)
	for _, msg := range model.calls[1] {
		for _, result := range msg.ToolResults {
			_, duplicate := results[result.Name]
			require.False(t, duplicate, "duplicate result for %s", result.Name)
			results[result.Name] = result
		}
	}
	return results
}

func TestPipeline_RealRegistryToolPolicies(t *testing.T) {
	// Reverse declaration order to catch a shared registry view whose MCP
	// permissions are accidentally overwritten by the last subagent.
	for _, mcpFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("mcpFirst=%t", mcpFirst), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			subs := []config.Subagent{
				{Title: "default", Prompt: "default instructions", LLM: config.LLMOverrides{Model: stringPointer("default")}},
				{Title: "enabled", Prompt: "MCP instructions", AllowMCPs: true, LLM: config.LLMOverrides{Model: stringPointer("enabled")}},
			}
			if mcpFirst {
				slices.Reverse(subs)
			}
			r, main, _ := newPipelineReviewer(t, &config.ReviewPipeline{MaxConcurrency: 2, Subagents: subs})
			r.Agent.reporter = &pipelineSafetyReporter{}
			registry := tools.NewRegistry()
			r.Agent.registry = registry
			executed := make(chan string, 9)
			for _, name := range []string{"builtin", "restricted", "mcp"} {
				handler := func(ctx context.Context, call llm.ToolCall) (string, error) {
					if err := ctx.Err(); err != nil {
						return "", err
					}
					scope := tools.SubagentFromContext(ctx)
					if scope == "" {
						scope = "main"
					}
					output := scope + ":" + call.Name
					executed <- output
					return output, nil
				}
				def := llm.ToolDef{Name: name, Description: name}
				switch name {
				case "restricted":
					registry.RegisterTool(def, handler, tools.ToolPolicy{AllowSubagents: false})
				case "mcp":
					registry.RegisterTool(def, handler, tools.ToolPolicy{AllowSubagents: true, IsMCP: true})
				default:
					registry.RegisterTool(def, handler)
				}
			}
			calls := []llm.ToolCall{
				makeToolCall("builtin-id", "builtin", nil),
				makeToolCall("restricted-id", "restricted", nil),
				makeToolCall("mcp-id", "mcp", nil),
			}
			// Request denied tools deliberately, even though they should not be
			// advertised. Filtering definitions alone is not authorization.
			models := map[string]*pipelineModel{}
			for _, title := range []string{"default", "enabled"} {
				models[title] = &pipelineModel{
					mockLLM: &mockLLM{responses: []*llm.Response{toolCallsResponse(calls...), textResponse(title + " report")}},
					before: func(ctx context.Context, _ []llm.Message, defs []llm.ToolDef, _ int) error {
						if tools.SubagentFromContext(ctx) != title {
							return fmt.Errorf("wrong subagent context: %q, want %q", tools.SubagentFromContext(ctx), title)
						}
						want := []string{"builtin"}
						if title == "enabled" {
							want = append(want, "mcp")
						}
						if got := pipelineSafetyToolNames(defs); !slices.Equal(want, got) {
							return fmt.Errorf("%s tools = %v, want %v", title, got, want)
						}
						return ctx.Err()
					},
				}
			}
			r.llmFactory = func(ctx context.Context, _, name, _, _ string, _ map[string]any) (llm.Model, error) {
				return models[name], ctx.Err()
			}
			main.responses = []*llm.Response{toolCallsResponse(calls...), textResponse("main review")}
			r.Agent.llm = &pipelineModel{mockLLM: main, before: func(ctx context.Context, _ []llm.Message, defs []llm.ToolDef, _ int) error {
				if tools.SubagentFromContext(ctx) != "" || !slices.Equal(pipelineSafetyToolNames(defs), []string{"builtin", "mcp", "restricted"}) {
					return fmt.Errorf("main inherited subagent scope or restricted tools: %v", pipelineSafetyToolNames(defs))
				}
				return ctx.Err()
			}}

			output, err := r.Run(ctx, []string{"file.go"}, "request")
			require.NoError(t, err)
			require.Equal(t, "main review", output)
			var actual []string
			for len(executed) > 0 {
				actual = append(actual, <-executed)
			}
			require.ElementsMatch(t, []string{"default:builtin", "enabled:builtin", "enabled:mcp", "main:builtin", "main:restricted", "main:mcp"}, actual)
			for _, title := range []string{"default", "enabled", "main"} {
				model := main
				if title != "main" {
					model = models[title].mockLLM
				}
				results := pipelineSafetyResults(t, model)
				require.Len(t, results, len(calls))
				for _, call := range calls {
					result := results[call.Name]
					require.Equal(t, call.ID, result.ToolCallID)
					denied := title != "main" && (call.Name == "restricted" || (call.Name == "mcp" && title == "default"))
					if denied {
						require.Contains(t, result.Content, "error:")
						require.NotContains(t, result.Content, title+":"+call.Name)
					} else {
						require.Contains(t, result.Content, title+":"+call.Name)
						require.NotContains(t, result.Content, "error:")
					}
				}
			}
		})
	}
}

func TestPipeline_RealRegistryConcurrentSameCallIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, main, _ := newPipelineReviewer(t, &config.ReviewPipeline{MaxConcurrency: 2, Subagents: []config.Subagent{
		{Title: "first", Prompt: "first instructions", LLM: config.LLMOverrides{Model: stringPointer("first")}},
		{Title: "second", Prompt: "second instructions", LLM: config.LLMOverrides{Model: stringPointer("second")}},
	}})
	registry := tools.NewRegistry()
	r.Agent.registry = registry
	entered := make(chan string, 2)
	release := make(chan struct{}, 1)
	registry.RegisterTool(llm.ToolDef{Name: "shared"}, func(ctx context.Context, call llm.ToolCall) (string, error) {
		var args struct {
			Source string `json:"source"`
		}
		if err := call.UnmarshalArguments(&args); err != nil {
			return "", err
		}
		if title := tools.SubagentFromContext(ctx); title != args.Source {
			return "", fmt.Errorf("context title = %q, want %q", title, args.Source)
		}
		entered <- args.Source
		select {
		case <-release:
			return "private output for " + args.Source, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	models := map[string]*mockLLM{}
	for _, title := range []string{"first", "second"} {
		models[title] = &mockLLM{responses: []*llm.Response{
			toolCallsResponse(makeToolCall("same-id", "shared", map[string]any{"source": title})),
			textResponse(title + " report"),
		}}
	}
	r.llmFactory = func(ctx context.Context, _, name, _, _ string, _ map[string]any) (llm.Model, error) {
		return models[name], ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, nil, "request")
		done <- err
	}()
	require.ElementsMatch(t, []string{"first", "second"}, []string{receivePipeline(t, entered), receivePipeline(t, entered)})
	close(release)
	require.NoError(t, receivePipeline(t, done))
	for title, model := range models {
		results := pipelineSafetyResults(t, model)
		require.Len(t, results, 1)
		require.Equal(t, "same-id", results["shared"].ToolCallID)
		require.Contains(t, results["shared"].Content, "private output for "+title)
		other := "first"
		if title == other {
			other = "second"
		}
		require.NotContains(t, results["shared"].Content, "private output for "+other)
		for _, msg := range model.calls[1] {
			for _, call := range msg.ToolCalls {
				require.Equal(t, "same-id", call.ID, "reporter namespacing must not alter provider IDs")
				require.Equal(t, "shared", call.Name)
			}
		}
	}
	require.Len(t, main.calls, 1)
}

func TestPipeline_InitialRunReporterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, main, _ := newPipelineReviewer(t, &config.ReviewPipeline{MaxConcurrency: 2, Subagents: []config.Subagent{
		{Title: "first", Prompt: "first instructions", LLM: config.LLMOverrides{Model: stringPointer("first")}},
		{Title: "second", Prompt: "second instructions", LLM: config.LLMOverrides{Model: stringPointer("second")}},
	}})
	reporter := &pipelineSafetyReporter{cancelUpdates: make(chan context.CancelFunc, 2)}
	r.Agent.reporter = reporter
	started := make(chan string, 2)
	exited := make(chan string, 2)
	r.llmFactory = func(ctx context.Context, _, name, _, _ string, _ map[string]any) (llm.Model, error) {
		return &pipelineModel{mockLLM: &mockLLM{}, before: func(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ int) error {
			if title := tools.SubagentFromContext(ctx); title != name {
				return fmt.Errorf("context title = %q, want %q", title, name)
			}
			started <- name
			<-ctx.Done()
			exited <- name
			return ctx.Err()
		}}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, nil, "request")
		done <- err
	}()
	reviewCancel := receivePipeline(t, reporter.cancelUpdates)
	require.NotNil(t, reviewCancel)
	require.ElementsMatch(t, []string{"first", "second"}, []string{receivePipeline(t, started), receivePipeline(t, started)})
	reviewCancel()
	require.ErrorIs(t, receivePipeline(t, done), context.Canceled)
	require.ElementsMatch(t, []string{"first", "second"}, []string{receivePipeline(t, exited), receivePipeline(t, exited)})
	require.Nil(t, receivePipeline(t, reporter.cancelUpdates), "finished review must clear its cancel callback")
	require.NoError(t, ctx.Err(), "cancel only the active review, not its caller")
	require.Empty(t, main.calls, "cancelling initial subagents must not start the main review")
	require.Nil(t, r.CodeReviewMetrics)
}
