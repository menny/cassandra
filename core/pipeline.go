package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/factory"
	"github.com/menny/cassandra/tools"
)

// subagentReport is deliberately a final-response handoff, not a transcript.
// JSON quoting keeps report text from breaking the surrounding boundaries.
type subagentReport struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
}

type subagentResult struct {
	index   int
	output  string
	metrics SessionMetrics
	err     error
}

func (r *Reviewer) loadPipeline() error {
	if r.Pipeline != nil || r.Config == nil || r.Config.ReviewPipelineFile == "" {
		return nil
	}
	pipeline, err := config.LoadReviewPipeline(r.Config.ReviewPipelineFile)
	if err != nil {
		return err
	}
	r.Pipeline = pipeline
	return nil
}

// runPipeline schedules at most MaxConcurrency independent ReAct flights. Only
// this coordinator publishes final reports and mutates aggregate review state.
func (r *Reviewer) runPipeline(ctx context.Context, dynamic, request string, maxIterations int) (string, error) {
	if len(r.Pipeline.Subagents) > 0 && r.Pipeline.MaxConcurrency <= 0 {
		return "", errors.New("review pipeline max_concurrency must be positive")
	}
	pipelineCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan subagentResult, min(r.Pipeline.MaxConcurrency, len(r.Pipeline.Subagents)))
	completed := make([]subagentResult, len(r.Pipeline.Subagents))
	finished := make([]bool, len(completed))
	var reportMu sync.Mutex
	next, running := 0, 0
	var requiredErr error

	launch := func() {
		for requiredErr == nil && pipelineCtx.Err() == nil && running < r.Pipeline.MaxConcurrency && next < len(completed) {
			index := next
			next++
			running++
			subagent := r.Pipeline.Subagents[index]
			reporter := &subagentReporter{
				Reporter: r.Agent.reporter,
				mu:       &reportMu,
				prefix:   fmt.Sprintf("subagent_%d_", index),
				stage:    "Subagent: " + subagent.Title,
			}
			go func() {
				results <- r.runSubagent(pipelineCtx, index, subagent, reporter, dynamic, request, maxIterations)
			}()
		}
	}

	launch()
	for running > 0 {
		result := <-results
		running--
		completed[result.index] = result
		finished[result.index] = true
		subagent := r.Pipeline.Subagents[result.index]
		if result.err != nil && subagent.Required && requiredErr == nil {
			requiredErr = fmt.Errorf("required subagent %q failed: %w", subagent.Title, result.err)
			cancel()
		}

		// All callbacks, including final output, share the lock so console
		// writers and reporters that were designed for one loop remain safe.
		reportMu.Lock()
		if result.err != nil {
			failure := fmt.Sprintf("Subagent %q failed: %v", subagent.Title, result.err)
			r.Agent.reporter.ReportWarning(failure, nil)
			if err := r.Agent.reporter.ReportStageReview(subagent.Title, failure); err != nil {
				r.Agent.reporter.ReportWarning("Failed to display subagent failure", err)
			}
		} else if subagent.UserVisible {
			if err := r.Agent.reporter.ReportStageReview(subagent.Title, result.output); err != nil {
				r.Agent.reporter.ReportWarning("Failed to display subagent report", err)
			}
		}
		reportMu.Unlock()
		launch()
	}

	var reports []subagentReport
	for i, result := range completed {
		if !finished[i] {
			continue
		}
		subagent := r.Pipeline.Subagents[i]
		r.appendSubagentMetrics(PhaseMetrics{
			Phase: "subagent", Title: subagent.Title, Metrics: result.metrics,
		})
		if !subagent.ReviewerInput && result.err == nil {
			continue
		}
		report := subagentReport{Title: subagent.Title, Description: subagent.Description, Output: result.output}
		if result.err != nil {
			report.Error = result.err.Error()
		}
		reports = append(reports, report)
	}
	if requiredErr != nil {
		return "", requiredErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(reports) == 0 {
		return request, nil
	}
	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding subagent reports: %w", err)
	}
	return request + "\n\n<subagent_reports>\n" + string(data) + "\n</subagent_reports>", nil
}

func (r *Reviewer) runSubagent(ctx context.Context, index int, subagent config.Subagent, reporter Reporter, dynamic, request string, maxIterations int) subagentResult {
	result := subagentResult{index: index}
	ctx = tools.WithSubagent(ctx, subagent.Title)
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	registry, ok := r.Agent.registry.(interface {
		ForSubagent(bool) tools.Dispatcher
	})
	if !ok {
		result.err = errors.New("tool dispatcher does not support subagent access policies")
		return result
	}
	resolved := subagent.ResolveLLM(r.Config)
	newModel := r.llmFactory
	if newModel == nil {
		newModel = factory.New
	}
	model, err := newModel(ctx, resolved.Provider, resolved.Model, resolved.ProviderAPIKey, resolved.ProviderURL, resolved.ProviderOptions)
	if err != nil {
		result.err = fmt.Errorf("initializing LLM: %w", err)
		return result
	}
	// Never share mutable model, conversation, metrics, or session persistence
	// with the main agent. Share only the policy-scoped tool environment.
	dispatcher := registry.ForSubagent(subagent.AllowMCPs)
	agent := NewAgent(model, dispatcher, WithReporter(reporter))
	agent.Stage = subagent.Title
	result.output, result.err = agent.RunReview(ctx, subagent.Prompt, dynamic, request, maxIterations, resolved.MaxTokens)
	result.metrics = agent.GetMetrics()
	return result
}

// subagentReporter serializes loop callbacks across subagents. Tool IDs are
// namespaced only for reporting; provider-facing correlation IDs stay intact.
type subagentReporter struct {
	Reporter
	mu     *sync.Mutex
	prefix string
	stage  string
	iter   int
}

func (r *subagentReporter) scopedCallID(id string) string {
	return fmt.Sprintf("%siter_%d_%s", r.prefix, r.iter, id)
}

func (r *subagentReporter) ReportStageIteration(stage string, iter int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stage = "Subagent: " + stage
	r.iter = iter
	r.Reporter.ReportStageIteration(r.stage, iter)
}

func (r *subagentReporter) ReportStageFinalReview(stage string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Reporter.ReportStageFinalReview("Subagent: " + stage)
}

func (r *subagentReporter) ReportToolCalls(calls []llm.ToolCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	scoped := append([]llm.ToolCall(nil), calls...)
	for i := range scoped {
		scoped[i].ID = r.scopedCallID(scoped[i].ID)
	}
	if staged, ok := r.Reporter.(interface {
		ReportStageToolCalls(stage string, iter int, calls []llm.ToolCall)
	}); ok {
		staged.ReportStageToolCalls(r.stage, r.iter, scoped)
	} else {
		r.Reporter.ReportToolCalls(scoped)
	}
}

func (r *subagentReporter) ReportToolStatus(call llm.ToolCall, status string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call.ID = r.scopedCallID(call.ID)
	r.Reporter.ReportToolStatus(call, status, err)
}

func (r *subagentReporter) ReportUsage(usage llm.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if staged, ok := r.Reporter.(interface {
		ReportStageUsage(string, llm.Usage)
	}); ok {
		staged.ReportStageUsage(r.stage, usage)
	} else {
		r.Reporter.ReportUsage(usage)
	}
}

func (r *subagentReporter) ReportUsageSummary(usage llm.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if staged, ok := r.Reporter.(interface {
		ReportStageUsageSummary(string, llm.Usage)
	}); ok {
		staged.ReportStageUsageSummary(r.stage, usage)
	} else {
		r.Reporter.ReportUsageSummary(usage)
	}
}

func (r *subagentReporter) ReportCapReached(cap int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if staged, ok := r.Reporter.(interface {
		ReportStageCapReached(string, int)
	}); ok {
		staged.ReportStageCapReached(r.stage, cap)
	} else {
		r.Reporter.ReportCapReached(cap)
	}
}

func (r *subagentReporter) ReportTruncated(tokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if staged, ok := r.Reporter.(interface {
		ReportStageTruncated(string, int)
	}); ok {
		staged.ReportStageTruncated(r.stage, tokens)
	} else {
		r.Reporter.ReportTruncated(tokens)
	}
}

func (r *subagentReporter) ReportEmptyResponseRetry(attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if staged, ok := r.Reporter.(interface {
		ReportStageEmptyResponseRetry(string, int, int)
	}); ok {
		staged.ReportStageEmptyResponseRetry(r.stage, r.iter, attempt)
	} else {
		r.Reporter.ReportWarning(fmt.Sprintf("[%s] Retrying empty response (attempt %d)", r.stage, attempt), nil)
	}
}

func (r *subagentReporter) ReportWarning(msg string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if staged, ok := r.Reporter.(interface {
		ReportStageWarning(string, string, error)
	}); ok {
		staged.ReportStageWarning(r.stage, msg, err)
	} else {
		r.Reporter.ReportWarning(fmt.Sprintf("[%s] %s", r.stage, msg), err)
	}
}
