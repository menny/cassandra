package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
	"github.com/stretchr/testify/require"
)

// funcModel is a goroutine-safe llm.Model whose behavior is a function of the
// call index and conversation. It forwards ctx like a real provider SDK.
type funcModel struct {
	mu    sync.Mutex
	calls [][]llm.Message
	defs  [][]llm.ToolDef
	fn    func(ctx context.Context, call int, msgs []llm.Message) (*llm.Response, error)
}

func (m *funcModel) GenerateContent(ctx context.Context, msgs []llm.Message, defs []llm.ToolDef, _ int) (*llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	call := len(m.calls)
	m.calls = append(m.calls, slices.Clone(msgs))
	m.defs = append(m.defs, slices.Clone(defs))
	m.mu.Unlock()
	return m.fn(ctx, call, msgs)
}

func (m *funcModel) GenerateStructuredContent(ctx context.Context, msgs []llm.Message, _ map[string]any, _ llm.StructuredConfig) (*llm.Response, error) {
	return m.GenerateContent(ctx, msgs, nil, 0)
}

func (m *funcModel) toolNames(call int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for _, def := range m.defs[call] {
		names = append(names, def.Name)
	}
	return names
}

// lockedReporter records stage-scoped callbacks that reach the main reporter.
type lockedReporter struct {
	Reporter
	mu       sync.Mutex
	stages   []string
	statusID []string
}

func (r *lockedReporter) ReportStageIteration(stage string, iter int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, fmt.Sprintf("%s|%d", stage, iter))
}

func (r *lockedReporter) ReportToolStatus(tc llm.ToolCall, status string, err error) {
	if status != "started" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusID = append(r.statusID, tc.ID)
}

func lastUserText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Text
		}
	}
	return ""
}

func subagentCall(id, title, task string) llm.ToolCall {
	return makeToolCall(id, runSubagentToolName, map[string]any{"title": title, "task": task})
}

type dynamicHarness struct {
	r        *Reviewer
	registry *tools.Registry
	main     *mockLLM
	reporter *lockedReporter
}

// newDynamicHarness wires a Reviewer around a real tools.Registry containing a
// built-in probe tool, a privileged ask_developer, and an MCP-style tool.
func newDynamicHarness(t *testing.T, cfg *config.Config, main *mockLLM, factory func(context.Context, string, string, string, string, map[string]any) (llm.Model, error)) *dynamicHarness {
	t.Helper()
	registry := tools.NewRegistry()
	handler := func(name string) tools.ToolHandler {
		return func(ctx context.Context, _ llm.ToolCall) (string, error) { return name + "-output", ctx.Err() }
	}
	registry.RegisterTool(llm.ToolDef{Name: "probe"}, handler("probe"))
	registry.RegisterTool(llm.ToolDef{Name: "ask_developer"}, handler("ask"), tools.ToolPolicy{AllowSubagents: false})
	registry.RegisterTool(llm.ToolDef{Name: "mcp_tool"}, handler("mcp"), tools.ToolPolicy{AllowSubagents: true, IsMCP: true})

	reporter := &lockedReporter{Reporter: NewRawReporter(io.Discard, io.Discard)}
	r := &Reviewer{
		Agent:        NewAgent(main, registry, WithReporter(reporter)),
		Config:       cfg,
		RootDir:      t.TempDir(),
		StablePrompt: "main instructions",
		Guidelines:   "general rules",
		llmFactory:   factory,
	}
	if cfg.AllowSubagents {
		r.registerRunSubagent(registry)
	}
	return &dynamicHarness{r: r, registry: registry, main: main, reporter: reporter}
}

func dynamicConfig() *config.Config {
	cfg := config.NewDefaultConfig()
	cfg.Provider = "anthropic"
	cfg.Model = "main-model"
	return cfg
}

// toolResults returns the content of tool results by call ID from the main
// model's second request.
func toolResults(t *testing.T, main *mockLLM) map[string]string {
	t.Helper()
	require.GreaterOrEqual(t, len(main.calls), 2)
	last := main.calls[1][len(main.calls[1])-1]
	require.Equal(t, llm.RoleTool, last.Role)
	results := map[string]string{}
	for _, res := range last.ToolResults {
		results[res.ToolCallID] = stripBudgetNote(res.Content)
	}
	return results
}

// stripBudgetNote removes the loop's iteration-budget note, which the agent
// appends to the last tool result of a turn.
func stripBudgetNote(content string) string {
	before, _, _ := strings.Cut(content, "\n\n---\n[SYSTEM NOTE]")
	return before
}

func TestDynamicSubagent_ReturnsOnlyFinalMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	main := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(subagentCall("main_1", "Trace auth", "find callers of Authenticate")),
		textResponse("main review"),
	}}
	sub := &funcModel{fn: func(_ context.Context, call int, _ []llm.Message) (*llm.Response, error) {
		if call == 0 {
			return &llm.Response{Text: "thinking out loud", ToolCalls: []llm.ToolCall{makeToolCall("s1", "probe", nil)}, Usage: llm.Usage{PromptTokens: 7, OutputTokens: 3}}, nil
		}
		return &llm.Response{Text: "FINAL: Authenticate is called from server.go:42", Usage: llm.Usage{PromptTokens: 5, OutputTokens: 2}}, nil
	}}
	h := newDynamicHarness(t, dynamicConfig(), main, func(context.Context, string, string, string, string, map[string]any) (llm.Model, error) {
		return sub, nil
	})

	out, err := h.r.Run(ctx, []string{"a.go"}, "review please")
	require.NoError(t, err)
	require.Equal(t, "main review", out)

	require.Equal(t, map[string]string{"main_1": "FINAL: Authenticate is called from server.go:42"}, toolResults(t, main))
	require.Len(t, sub.calls, 2)
	require.Equal(t, prompts.BuildDynamicSubagentPrompt(), sub.calls[0][0].Text)
	require.Equal(t, "find callers of Authenticate", lastUserText(sub.calls[0]))
	require.Equal(t, "probe-output", stripBudgetNote(sub.calls[1][len(sub.calls[1])-1].ToolResults[0].Content))
	require.Empty(t, h.r.Agent.History()[0].ToolResults, "sub history must not leak into main history")

	metrics := h.r.GetMetrics()
	require.Len(t, metrics.Phases, 2)
	require.Equal(t, "dynamic_subagent", metrics.Phases[0].Phase)
	require.Equal(t, "Trace auth", metrics.Phases[0].Title)
	require.Equal(t, 12, metrics.Phases[0].Metrics.Tokens.Input)
	require.Equal(t, 2, metrics.Phases[0].Metrics.Iterations)
	require.Equal(t, 1, metrics.Phases[0].Metrics.ToolCalls.ByTool["probe"])
	require.Equal(t, "review", metrics.Phases[1].Phase)
	require.Zero(t, metrics.Phases[1].Metrics.ToolCalls.ByTool["probe"])
}

func TestDynamicSubagent_ParallelRespectsConcurrencyCap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pipeline *config.ReviewPipeline
		limit    int
	}{
		{name: "default", limit: config.DefaultPipelineMaxConcurrency},
		{name: "pipeline", pipeline: &config.ReviewPipeline{MaxConcurrency: 2}, limit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const total = 6
			var calls []llm.ToolCall
			for i := range total {
				calls = append(calls, subagentCall(fmt.Sprintf("main_%d", i), "Same title", fmt.Sprintf("task-%d", i)))
			}
			main := &mockLLM{responses: []*llm.Response{toolCallsResponse(calls...), textResponse("done")}}

			var active, peak atomic.Int32
			started := make(chan struct{}, total)
			release := make(chan struct{})
			factory := func(context.Context, string, string, string, string, map[string]any) (llm.Model, error) {
				return &funcModel{fn: func(ctx context.Context, call int, msgs []llm.Message) (*llm.Response, error) {
					if call > 0 {
						return textResponse("answer for " + lastUserText(msgs)), nil
					}
					now := active.Add(1)
					for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
					}
					started <- struct{}{}
					defer active.Add(-1)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					// Every subagent reuses the same provider tool-call ID.
					return toolCallsResponse(makeToolCall("same_id", "probe", nil)), nil
				}}, nil
			}
			h := newDynamicHarness(t, dynamicConfig(), main, factory)
			h.r.Pipeline = tc.pipeline

			done := make(chan error, 1)
			go func() {
				_, err := h.r.Run(ctx, []string{"a.go"}, "request")
				done <- err
			}()
			for range tc.limit {
				receivePipeline(t, started)
			}
			select {
			case <-started:
				t.Fatalf("more than %d subagents ran concurrently", tc.limit)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			require.NoError(t, <-done)
			require.EqualValues(t, tc.limit, peak.Load())

			results := toolResults(t, main)
			require.Len(t, results, total)
			for i := range total {
				require.Equal(t, fmt.Sprintf("answer for task-%d", i), results[fmt.Sprintf("main_%d", i)])
			}
			require.Len(t, h.r.GetMetrics().Phases, total+1)

			// Identical titles and provider IDs must stay distinguishable.
			h.reporter.mu.Lock()
			defer h.reporter.mu.Unlock()
			stages := map[string]bool{}
			for _, s := range h.reporter.stages {
				stages[strings.SplitN(s, "|", 2)[0]] = true
			}
			require.Len(t, stages, total)
			for stage := range stages {
				require.True(t, strings.HasPrefix(stage, "Subagent: Same title #"), stage)
			}
			seen := map[string]bool{}
			for _, id := range h.reporter.statusID {
				if strings.HasPrefix(id, "dynamic_subagent_") {
					require.False(t, seen[id], "duplicate scoped id %s", id)
					seen[id] = true
				}
			}
			require.Len(t, seen, total)
		})
	}
}

func TestDynamicSubagent_ToolIsolation(t *testing.T) {
	for _, allowMCPs := range []bool{true, false} {
		t.Run(fmt.Sprintf("mcps=%t", allowMCPs), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := dynamicConfig()
			cfg.AllowSubagentMCPs = allowMCPs
			main := &mockLLM{responses: []*llm.Response{
				toolCallsResponse(subagentCall("m1", "Probe", "probe everything")),
				textResponse("done"),
			}}
			sub := &funcModel{fn: func(_ context.Context, call int, _ []llm.Message) (*llm.Response, error) {
				if call == 0 {
					return toolCallsResponse(
						makeToolCall("a", runSubagentToolName, map[string]any{"title": "x", "task": "y"}),
						makeToolCall("b", "ask_developer", map[string]any{"question": "q", "reasoning": "r"}),
						makeToolCall("c", "mcp_tool", nil),
					), nil
				}
				return textResponse("finished"), nil
			}}
			h := newDynamicHarness(t, cfg, main, func(context.Context, string, string, string, string, map[string]any) (llm.Model, error) {
				return sub, nil
			})
			_, err := h.r.Run(ctx, []string{"a.go"}, "request")
			require.NoError(t, err)

			names := sub.toolNames(0)
			require.Contains(t, names, "probe")
			require.NotContains(t, names, runSubagentToolName)
			require.NotContains(t, names, "ask_developer")
			require.Equal(t, allowMCPs, slices.Contains(names, "mcp_tool"))
			require.Contains(t, h.registry.ToTools(), llm.ToolDef{Name: "ask_developer"})
			mainNames := func() (n []string) {
				for _, d := range h.registry.ToTools() {
					n = append(n, d.Name)
				}
				return
			}()
			require.Contains(t, mainNames, runSubagentToolName)

			results := map[string]string{}
			for _, res := range sub.calls[1][len(sub.calls[1])-1].ToolResults {
				results[res.ToolCallID] = stripBudgetNote(res.Content)
			}
			require.Contains(t, results["a"], "not allowed for subagents")
			require.Contains(t, results["b"], "not allowed for subagents")
			if allowMCPs {
				require.Equal(t, "mcp-output", results["c"])
			} else {
				require.Contains(t, results["c"], "not allowed for subagents")
			}
		})
	}
}

func TestDynamicSubagent_ModelResolution(t *testing.T) {
	for _, override := range []string{"", "cheap-model"} {
		t.Run("override="+override, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := dynamicConfig()
			cfg.ProviderAPIKey = "key"
			cfg.ProviderURL = "http://example.test"
			cfg.MaxTokens = 1234
			cfg.ProviderOptions = map[string]any{"nested": map[string]any{"list": []any{"a"}}}
			cfg.SubagentModel = override
			main := &mockLLM{responses: []*llm.Response{
				toolCallsResponse(subagentCall("m1", "T", "task")),
				textResponse("done"),
			}}
			var gotModel, gotProvider, gotKey, gotURL string
			var gotOptions map[string]any
			h := newDynamicHarness(t, cfg, main, func(_ context.Context, provider, model, key, url string, options map[string]any) (llm.Model, error) {
				gotProvider, gotModel, gotKey, gotURL, gotOptions = provider, model, key, url, options
				return &funcModel{fn: func(context.Context, int, []llm.Message) (*llm.Response, error) {
					return textResponse("ok"), nil
				}}, nil
			})
			_, err := h.r.Run(ctx, []string{"a.go"}, "request")
			require.NoError(t, err)

			want := "main-model"
			if override != "" {
				want = override
			}
			require.Equal(t, want, gotModel)
			require.Equal(t, "anthropic", gotProvider)
			require.Equal(t, "key", gotKey)
			require.Equal(t, "http://example.test", gotURL)
			require.Equal(t, cfg.ProviderOptions, gotOptions)

			gotOptions["nested"].(map[string]any)["list"].([]any)[0] = "mutated"
			require.Equal(t, "a", cfg.ProviderOptions["nested"].(map[string]any)["list"].([]any)[0], "options must be deep cloned")
			require.Equal(t, "main-model", cfg.Model, "main config must not change")
		})
	}
}

func TestDynamicSubagent_FailureDoesNotAffectSiblings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	main := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(
			subagentCall("bad", "Bad", "explode"),
			subagentCall("good", "Good", "work"),
			subagentCall("nofactory", "NoFactory", "init-fails"),
			makeToolCall("empty", runSubagentToolName, map[string]any{"title": " ", "task": "x"}),
		),
		textResponse("main survived"),
	}}
	factory := func(_ context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
		return &funcModel{fn: func(_ context.Context, _ int, msgs []llm.Message) (*llm.Response, error) {
			switch lastUserText(msgs) {
			case "explode":
				return nil, errors.New("provider exploded")
			case "init-fails":
				return textResponse("unreachable"), nil
			}
			return textResponse("good result"), nil
		}}, nil
	}
	h := newDynamicHarness(t, dynamicConfig(), main, factory)
	out, err := h.r.Run(ctx, []string{"a.go"}, "request")
	require.NoError(t, err)
	require.Equal(t, "main survived", out)

	results := toolResults(t, main)
	require.Contains(t, results["bad"], "error:")
	require.Contains(t, results["bad"], "provider exploded")
	require.Equal(t, "good result", results["good"])
	require.Contains(t, results["empty"], "title must not be empty")

	phases := h.r.GetMetrics().Phases
	var titles []string
	for _, p := range phases {
		if p.Phase == "dynamic_subagent" {
			titles = append(titles, p.Title)
		}
	}
	require.ElementsMatch(t, []string{"Bad", "Good", "NoFactory"}, titles, "failed subagents still report usage")
}

func TestDynamicSubagent_FactoryErrorIsToolError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	main := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(subagentCall("m1", "T", "task")),
		textResponse("done"),
	}}
	h := newDynamicHarness(t, dynamicConfig(), main, func(context.Context, string, string, string, string, map[string]any) (llm.Model, error) {
		return nil, errors.New("no credentials")
	})
	_, err := h.r.Run(ctx, []string{"a.go"}, "request")
	require.NoError(t, err)
	require.Contains(t, toolResults(t, main)["m1"], "no credentials")
}

func TestDynamicSubagent_CancellationStopsRunningAndQueued(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const total = 7
	var calls []llm.ToolCall
	for i := range total {
		calls = append(calls, subagentCall(fmt.Sprintf("m%d", i), fmt.Sprintf("T%d", i), "task"))
	}
	main := &mockLLM{responses: []*llm.Response{toolCallsResponse(calls...), textResponse("unreachable")}}
	var factoryCalls atomic.Int32
	started := make(chan struct{}, total)
	h := newDynamicHarness(t, dynamicConfig(), main, func(context.Context, string, string, string, string, map[string]any) (llm.Model, error) {
		factoryCalls.Add(1)
		return &funcModel{fn: func(ctx context.Context, _ int, _ []llm.Message) (*llm.Response, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}}, nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := h.r.Run(ctx, []string{"a.go"}, "request")
		done <- err
	}()
	for range config.DefaultPipelineMaxConcurrency {
		receivePipeline(t, started)
	}
	cancel()
	require.ErrorIs(t, receivePipeline(t, done), context.Canceled)
	require.EqualValues(t, config.DefaultPipelineMaxConcurrency, factoryCalls.Load(), "queued subagents must never start")
}

func TestDynamicSubagent_AcquireSlotHonorsContext(t *testing.T) {
	r := &Reviewer{Config: dynamicConfig(), Pipeline: &config.ReviewPipeline{MaxConcurrency: 1}}
	release, err := r.acquireSubagentSlot(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := r.acquireSubagentSlot(ctx)
		errCh <- err
	}()
	cancel()
	require.ErrorIs(t, receivePipeline(t, errCh), context.Canceled)
	release()

	// A canceled context never wins a free slot.
	_, err = r.acquireSubagentSlot(ctx)
	require.ErrorIs(t, err, context.Canceled)
	release, err = r.acquireSubagentSlot(context.Background())
	require.NoError(t, err)
	release()
}

func TestDynamicSubagent_WorksDuringPostReviewChat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := dynamicConfig()
	main := &mockLLM{responses: []*llm.Response{
		toolCallsResponse(subagentCall("m1", "Chat research", "look into X")),
		textResponse("chat reply"),
	}}
	h := newDynamicHarness(t, cfg, main, func(context.Context, string, string, string, string, map[string]any) (llm.Model, error) {
		return &funcModel{fn: func(context.Context, int, []llm.Message) (*llm.Response, error) {
			return textResponse("X is fine"), nil
		}}, nil
	})
	h.r.Agent.history = []llm.Message{
		{Role: llm.RoleSystem, Text: "stable"},
		{Role: llm.RoleUser, Text: "request"},
		{Role: llm.RoleAssistant, Text: "review"},
	}

	inR, inW := io.Pipe()
	var out bytes.Buffer
	replCtx := WithTestREPLStreams(ctx, inR, &out)
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("check X please\n.\n"))
		time.Sleep(100 * time.Millisecond)
		_, _ = inW.Write([]byte("exit\n.\n"))
	}()
	require.NoError(t, h.r.RunInteractivePostReview(replCtx))

	require.Equal(t, "X is fine", toolResults(t, main)["m1"])
	phases := h.r.GetMetrics().Phases
	require.Len(t, phases, 2)
	require.Equal(t, "dynamic_subagent", phases[0].Phase)
	require.Equal(t, "review_discussion", phases[1].Phase)
}

func TestNewReviewer_RegistersRunSubagentOnlyWhenAllowed(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := config.NewDefaultConfig()
			cfg.Provider = "openai"
			cfg.Model = "test-model"
			cfg.ProviderAPIKey = "test-key"
			cfg.AllowSubagents = allow
			cfg.AllowAskDeveloper = true
			r, err := NewReviewer(ctx, cfg, t.TempDir(), NewRawReporter(io.Discard, io.Discard))
			require.NoError(t, err)
			defer r.Close()

			var mainNames, subNames []string
			for _, def := range r.Agent.registry.ToTools() {
				mainNames = append(mainNames, def.Name)
			}
			view := r.Agent.registry.(*tools.Registry).ForSubagent(true)
			for _, def := range view.ToTools() {
				subNames = append(subNames, def.Name)
			}
			require.Equal(t, allow, slices.Contains(mainNames, runSubagentToolName))
			require.Equal(t, allow, strings.Contains(r.StablePrompt, "<subagent_guidelines>"))
			require.Contains(t, mainNames, "ask_developer")
			require.NotContains(t, subNames, runSubagentToolName)
			require.NotContains(t, subNames, "ask_developer")
			_, err = view.HandleCall(ctx, llm.ToolCall{Name: runSubagentToolName, Arguments: "{}"})
			require.Error(t, err)
		})
	}
}

func TestParseRunSubagentAndFormatting(t *testing.T) {
	tc := subagentCall("1", "Trace auth", "line one\n  line two")
	title, task, ok := parseRunSubagent(tc)
	require.True(t, ok)
	require.Equal(t, "Trace auth", title)
	require.Equal(t, "line one\n  line two", task)
	_, _, ok = parseRunSubagent(llm.ToolCall{Name: "read_file"})
	require.False(t, ok)
	require.Equal(t, "line one line two", compactSubagentTask(task, 100))
	require.Equal(t, "line…", compactSubagentTask(task, 4))
	require.Equal(t, "Subagent: X", subagentToolTitle("X"))
	require.Equal(t, "Subagent", subagentToolTitle(""))
}
