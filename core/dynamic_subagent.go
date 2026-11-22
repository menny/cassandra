package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/factory"
	"github.com/menny/cassandra/tools"
)

const (
	runSubagentToolName = "run_subagent"

	// dynamicSubagentMinIterations is the floor for a dynamic subagent's
	// iteration cap, so small PRs still allow a meaningful investigation.
	dynamicSubagentMinIterations = 10
)

// runSubagentArgs are the arguments of the run_subagent tool.
type runSubagentArgs struct {
	Title string `json:"title"`
	Task  string `json:"task"`
}

// subagentDispatcherProvider is implemented by dispatchers that can derive a
// policy-scoped view for subagents (*tools.Registry).
type subagentDispatcherProvider interface {
	ForSubagent(allowMCPs bool) tools.Dispatcher
}

// registerRunSubagent adds the run_subagent tool to registry. It must be called
// during setup, before any concurrent dispatch, because the registry is not
// synchronized. The tool is denied to subagents so they cannot recurse.
func (r *Reviewer) registerRunSubagent(registry *tools.Registry) {
	def := llm.ToolDef{
		Name: runSubagentToolName,
		Description: "Run a focused research subagent with a fresh context and wait for its answer. " +
			"The subagent sees only the `task` you provide, can use read-only repository tools, and only its final message is returned to you. " +
			"Call this several times in one turn to research independent questions in parallel.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Short label describing the subagent's job (used for progress reporting).",
				},
				"task": map[string]any{
					"type":        "string",
					"description": "Complete, self-contained instructions: context, exact file paths and symbols, the specific questions to answer, and the desired answer format.",
				},
			},
			"required": []string{"title", "task"},
		},
	}
	tools.RegisterToolWithArgs(registry, def, r.runDynamicSubagent, tools.ToolPolicy{AllowSubagents: false})
}

// setDynamicSubagentContext records the per-review Zone 3 prompt and iteration
// cap used by subsequently spawned dynamic subagents.
func (r *Reviewer) setDynamicSubagentContext(dynamicPrompt string, changedFiles int) {
	r.dynamicMu.Lock()
	defer r.dynamicMu.Unlock()
	r.dynamicPrompt = dynamicPrompt
	r.dynamicMaxIterations = max(dynamicSubagentMinIterations, CalculateMaxIterations(changedFiles))
}

func (r *Reviewer) dynamicSubagentContext() (dynamicPrompt string, maxIterations int) {
	r.dynamicMu.Lock()
	defer r.dynamicMu.Unlock()
	maxIterations = r.dynamicMaxIterations
	if maxIterations <= 0 {
		maxIterations = dynamicSubagentMinIterations
	}
	return r.dynamicPrompt, maxIterations
}

// acquireSubagentSlot blocks until a concurrency slot is free or ctx ends. The
// cap matches the static pipeline's so both share one concurrency budget policy.
func (r *Reviewer) acquireSubagentSlot(ctx context.Context) (release func(), err error) {
	r.subagentSemOnce.Do(func() {
		limit := config.DefaultPipelineMaxConcurrency
		if r.Pipeline != nil && r.Pipeline.MaxConcurrency > 0 {
			limit = r.Pipeline.MaxConcurrency
		}
		r.subagentSem = make(chan struct{}, limit)
	})
	select {
	case r.subagentSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// select picks randomly when both cases are ready; never start after cancel.
	if err := ctx.Err(); err != nil {
		<-r.subagentSem
		return nil, err
	}
	return func() { <-r.subagentSem }, nil
}

// dynamicSubagentLLM resolves the LLM settings for a dynamic subagent: the main
// agent's settings, with only the model replaced when SubagentModel is set.
func (r *Reviewer) dynamicSubagentLLM() config.ResolvedLLM {
	var spec config.Subagent
	if r.Config != nil && r.Config.SubagentModel != "" {
		model := r.Config.SubagentModel
		spec.LLM.Model = &model
	}
	return spec.ResolveLLM(r.Config)
}

// runDynamicSubagent executes one independent ReAct flight and returns only the
// subagent's final message. Failures are returned as errors so the main agent
// sees them as a tool result and can continue.
func (r *Reviewer) runDynamicSubagent(ctx context.Context, args runSubagentArgs) (string, error) {
	title := strings.TrimSpace(args.Title)
	task := strings.TrimSpace(args.Task)
	if title == "" {
		return "", errors.New("title must not be empty")
	}
	if task == "" {
		return "", errors.New("task must not be empty")
	}

	release, err := r.acquireSubagentSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	provider, ok := r.Agent.registry.(subagentDispatcherProvider)
	if !ok {
		return "", errors.New("tool dispatcher does not support subagent access policies")
	}
	allowMCPs := r.Config != nil && r.Config.AllowSubagentMCPs
	dispatcher := provider.ForSubagent(allowMCPs)

	resolved := r.dynamicSubagentLLM()
	newModel := r.llmFactory
	if newModel == nil {
		newModel = factory.New
	}
	model, err := newModel(ctx, resolved.Provider, resolved.Model, resolved.ProviderAPIKey, resolved.ProviderURL, resolved.ProviderOptions)
	if err != nil {
		return "", fmt.Errorf("initializing LLM for subagent %q: %w", title, err)
	}

	// The sequence number keeps concurrent subagents with identical titles
	// distinct in reporter state (TUI iterations, ACP pending tool calls).
	seq := r.dynamicSubagentSeq.Add(1)
	stage := fmt.Sprintf("%s #%d", title, seq)
	reporter := &subagentReporter{
		Reporter: r.Agent.reporter,
		mu:       &r.reportMu,
		prefix:   fmt.Sprintf("dynamic_subagent_%d_", seq),
		stage:    "Subagent: " + stage,
	}

	ctx = tools.WithSubagent(ctx, title)
	dynamicPrompt, maxIterations := r.dynamicSubagentContext()
	// Fresh agent: no shared history, metrics, or session persistence.
	agent := NewAgent(model, dispatcher, WithReporter(reporter))
	agent.Stage = stage
	output, runErr := agent.RunReview(ctx, prompts.BuildDynamicSubagentPrompt(), dynamicPrompt, task, maxIterations, resolved.MaxTokens)
	r.appendSubagentMetrics(PhaseMetrics{Phase: "dynamic_subagent", Title: title, Metrics: agent.GetMetrics()})
	if runErr != nil {
		return "", fmt.Errorf("subagent %q failed: %w", title, runErr)
	}
	return output, nil
}

// appendSubagentMetrics records phase metrics; safe for concurrent callers.
func (r *Reviewer) appendSubagentMetrics(phase PhaseMetrics) {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	r.SubagentMetrics = append(r.SubagentMetrics, phase)
}

func (r *Reviewer) resetSubagentMetrics() {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	r.SubagentMetrics = nil
}

func (r *Reviewer) subagentMetricsSnapshot() []PhaseMetrics {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	return append([]PhaseMetrics(nil), r.SubagentMetrics...)
}

// subagentIcon marks dynamic subagent activity in console, TUI, and ACP output.
const subagentIcon = "🛰️"

// parseRunSubagent extracts the title and task if tc is a run_subagent call.
// Malformed arguments yield empty fields but still report ok so renderers keep
// the subagent presentation instead of dumping raw JSON.
func parseRunSubagent(tc llm.ToolCall) (title, task string, ok bool) {
	if tc.Name != runSubagentToolName {
		return "", "", false
	}
	var args runSubagentArgs
	_ = tc.UnmarshalArguments(&args)
	return strings.TrimSpace(args.Title), strings.TrimSpace(args.Task), true
}

// compactSubagentTask flattens a task to one line of at most maxLen runes.
func compactSubagentTask(task string, maxLen int) string {
	flat := strings.Join(strings.Fields(task), " ")
	if runes := []rune(flat); len(runes) > maxLen {
		return string(runes[:maxLen]) + "…"
	}
	return flat
}

// subagentToolTitle is the display title for a run_subagent call.
func subagentToolTitle(title string) string {
	if title == "" {
		return "Subagent"
	}
	return "Subagent: " + title
}
