package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/menny/cassandra/llm"
)

const (
	// MaxIterationsPerFile is the recommended number of iterations per changed file.
	MaxIterationsPerFile = 5
	// AbsoluteMaxIter is the upper bound for the ReAct loop to prevent infinite recursion.
	AbsoluteMaxIter = 25
)

// CalculateMaxIterations returns a sensible iteration cap based on the number
// of changed files, bounded by AbsoluteMaxIter.
func CalculateMaxIterations(changedFiles int) int {
	if changedFiles <= 0 {
		return 1
	}
	return min(MaxIterationsPerFile*changedFiles, AbsoluteMaxIter)
}

// ToolDispatcher is the minimal interface the Agent needs from a tool registry.
// *tools.Registry satisfies this interface; tests can supply a lightweight stub.
type ToolDispatcher interface {
	ToTools() []llm.ToolDef
	HandleCall(tc llm.ToolCall) (string, error)
}

// Reporter defines how the Agent reports progress and diagnostics.
type Reporter interface {
	ReportIteration(iter int)
	ReportToolCall(tc llm.ToolCall)
	ReportUsage(usage llm.Usage)
	ReportUsageSummary(usage llm.Usage)
	ReportFinalReview()
	ReportExtraction()
	ReportExtractionRetry(attempt int)
	ReportEmptyResponseRetry(attempt int)
	ReportCapReached(maxIterations int)
}

// defaultReporter writes progress to an io.Writer.
type defaultReporter struct {
	w io.Writer
}

func (r *defaultReporter) ReportIteration(iter int) {
	fmt.Fprintf(r.w, "Iteration %d: Cassandra is reviewing the code...\n", iter)
}

func (r *defaultReporter) ReportToolCall(tc llm.ToolCall) {
	fmt.Fprintf(r.w, "Cassandra asked to run tool %q (%s)\n", tc.Name, compactToolCallArgs(tc))
}

func (r *defaultReporter) ReportUsage(usage llm.Usage) {
	if usage.PromptTokens >= 0 && usage.OutputTokens >= 0 {
		msg := fmt.Sprintf("  [Tokens: %d input, %d output]", usage.TotalInput(), usage.TotalOutput())
		var breakdown []string
		if usage.CachedTokens > 0 {
			breakdown = append(breakdown, fmt.Sprintf("%d cached", usage.CachedTokens))
		}
		if usage.ThinkingTokens > 0 {
			breakdown = append(breakdown, fmt.Sprintf("%d thinking", usage.ThinkingTokens))
		}
		if len(breakdown) > 0 {
			msg += " (" + strings.Join(breakdown, ", ") + ")"
		}
		fmt.Fprintln(r.w, msg)
	}
}

func (r *defaultReporter) ReportUsageSummary(total llm.Usage) {
	if total.PromptTokens > 0 || total.OutputTokens > 0 {
		fmt.Fprintf(r.w, "Total session tokens: %d input, %d output\n", total.TotalInput(), total.TotalOutput())
	}
}

func (r *defaultReporter) ReportFinalReview() {
	fmt.Fprintln(r.w, "Cassandra is formulating the final review...")
}

func (r *defaultReporter) ReportExtraction() {
	fmt.Fprintln(r.w, "Cassandra is extracting structured JSON findings...")
}

func (r *defaultReporter) ReportExtractionRetry(attempt int) {
	fmt.Fprintf(r.w, "Extraction attempt %d failed; retrying...\n", attempt)
}

func (r *defaultReporter) ReportEmptyResponseRetry(attempt int) {
	fmt.Fprintf(r.w, "LLM returned empty response (attempt %d); retrying...\n", attempt)
}

func (r *defaultReporter) ReportCapReached(maxIterations int) {
	fmt.Fprintf(r.w, "Warning: reached maximum ReAct iterations (%d). Forcing final review.\n", maxIterations)
}

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// WithStderr redirects diagnostic/progress output to w instead of os.Stderr.
// Useful in tests to suppress noise (pass io.Discard).
func WithStderr(w io.Writer) AgentOption {
	return func(a *Agent) { a.reporter = &defaultReporter{w: w} }
}

// WithReporter sets a custom reporter for the Agent.
func WithReporter(r Reporter) AgentOption {
	return func(a *Agent) { a.reporter = r }
}

// Agent orchestrates the ReAct (Reason + Act) loop between the LLM and the tool registry.
type Agent struct {
	llm        llm.Model
	registry   ToolDispatcher
	reporter   Reporter
	totalUsage llm.Usage
}

// NewAgent creates an Agent. Diagnostic / progress output goes to os.Stderr by
// default; override with WithStderr or WithReporter. The final review is
// returned as a string (caller routes it to stdout).
func NewAgent(model llm.Model, registry ToolDispatcher, opts ...AgentOption) *Agent {
	a := &Agent{
		llm:      model,
		registry: registry,
		reporter: &defaultReporter{w: os.Stderr},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// RunReview executes the ReAct loop.
// stableSystem is the stable prompt prefix (Zones 1+2); dynamicSystem is the
// per-PR dynamic suffix (Zone 3, e.g. AGENTS.md / REVIEWERS.md content).
// When dynamicSystem is non-empty, providers that support prompt caching
// (e.g. Anthropic) will cache the stable prefix. Pass an empty string for
// dynamicSystem when there is no per-PR dynamic content.
// maxIterations controls how many tool-call rounds are permitted before the
// loop is forcibly terminated. Pass 0 to use the default cap.
// maxTokens limits the length of the LLM response.
func (a *Agent) RunReview(ctx context.Context, stableSystem, dynamicSystem, requestText string, maxIterations, maxTokens int) (string, error) {
	if maxIterations <= 0 {
		maxIterations = AbsoluteMaxIter
	}
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	var messages []llm.Message
	if dynamicSystem != "" {
		messages = []llm.Message{
			{Role: llm.RoleSystem, Text: stableSystem, CacheBreakpoint: true},
			{Role: llm.RoleSystem, Text: dynamicSystem},
			{Role: llm.RoleUser, Text: requestText},
		}
	} else {
		messages = []llm.Message{
			{Role: llm.RoleSystem, Text: stableSystem, CacheBreakpoint: true},
			{Role: llm.RoleUser, Text: requestText},
		}
	}

	tools := a.registry.ToTools()

	defer func() {
		a.reporter.ReportUsageSummary(a.totalUsage)
	}()

	for iter := range maxIterations {
		a.reporter.ReportIteration(iter + 1)

		resp, err := a.generateContentWithEmptyRetry(ctx, messages, tools, maxTokens)
		if err != nil {
			return "", fmt.Errorf("llm call failed on iteration %d: %w", iter+1, err)
		}

		a.reporter.ReportUsage(resp.Usage)
		a.trackUsage(resp.Usage)

		// No tool calls → LLM has produced its final review.
		if len(resp.ToolCalls) == 0 {
			if resp.Text == "" {
				return "", fmt.Errorf("llm returned empty content on iteration %d after %d attempts", iter+1, emptyResponseMaxAttempts)
			}
			a.reporter.ReportFinalReview()
			return resp.Text, nil
		}

		// ── Handle tool calls ────────────────────────────────────────────────

		// Append the assistant's tool-call turn to history.
		messages = append(messages, llm.Message{
			Role:             llm.RoleAssistant,
			Text:             resp.Text,
			ToolCalls:        resp.ToolCalls,
			Reasoning:        resp.Reasoning,
			ProviderMetadata: resp.ProviderMetadata,
		})

		toolMsg, err := a.executeToolCalls(resp.ToolCalls)
		if err != nil {
			return "", err
		}
		messages = append(messages, toolMsg)
	}

	return a.handleCapReached(ctx, messages, maxIterations, maxTokens)
}

// extractionMaxAttempts is the number of total attempts for structured extraction
// (LLM call + JSON parse), including the initial attempt.
const extractionMaxAttempts = 3

// emptyResponseMaxAttempts is the number of total attempts when a provider
// returns a successful (nil-error) response with empty content. This is a
// distinct failure mode from a hard error and needs its own retry budget.
const emptyResponseMaxAttempts = 3

// ExtractStructuredReview takes a raw markdown review and converts it into a
// machine-readable StructuredReview using a second LLM pass.
// If the LLM returns malformed JSON, the call is retried up to extractionMaxAttempts
// times total so that non-determinism in the LLM output can be overcome.
func (a *Agent) ExtractStructuredReview(ctx context.Context, extractionSystemPrompt, rawReview string, config llm.StructuredConfig) (*StructuredReview, error) {
	a.reporter.ReportExtraction()

	messages := []llm.Message{
		{Role: llm.RoleSystem, Text: extractionSystemPrompt},
		{Role: llm.RoleUser, Text: rawReview},
	}

	var lastErr error
	for attempt := range extractionMaxAttempts {
		if attempt > 0 {
			a.reporter.ReportExtractionRetry(attempt + 1)
		}

		resp, err := a.llm.GenerateStructuredContent(ctx, messages, StructuredReviewSchema, config)
		if err != nil {
			lastErr = fmt.Errorf("extraction failed: %w", err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}

		a.reporter.ReportUsage(resp.Usage)
		a.trackUsage(resp.Usage)

		if resp.Text == "" {
			lastErr = fmt.Errorf("extraction returned empty content")
			continue
		}

		var review StructuredReview
		if err := json.Unmarshal([]byte(resp.Text), &review); err != nil {
			lastErr = fmt.Errorf("failed to parse structured review: %w\nRaw output: %s", err, resp.Text)
			continue
		}

		return &review, nil
	}

	return nil, lastErr
}

func (a *Agent) executeToolCalls(toolCalls []llm.ToolCall) (llm.Message, error) {
	// Execute all tool calls and collect results into ONE RoleTool message.
	// All ToolResults must be in a single message so providers see strict
	// role alternation (no consecutive same-role turns).
	toolMsg := llm.Message{
		Role:        llm.RoleTool,
		ToolResults: make([]llm.ToolResult, 0, len(toolCalls)),
	}

	for _, tc := range toolCalls {
		// Progress line: print tool name + a compact summary of args.
		a.reporter.ReportToolCall(tc)

		// Dispatch; on error, surface the message as the tool result so the
		// LLM can reason about it rather than crashing the whole loop.
		result, toolErr := a.registry.HandleCall(tc)
		if toolErr != nil {
			result = fmt.Sprintf("error: %v", toolErr)
		}

		toolMsg.ToolResults = append(toolMsg.ToolResults, llm.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    result,
		})
	}
	return toolMsg, nil
}

func (a *Agent) handleCapReached(ctx context.Context, messages []llm.Message, maxIterations, maxTokens int) (string, error) {
	// ── Cap reached ─────────────────────────────────────────────────────────
	capMsg := fmt.Sprintf(
		"[SYSTEM] The maximum number of tool-call iterations (%d) has been reached. "+
			"You MUST now produce your final code review unconditionally, based on everything "+
			"you have gathered so far. Do not request any additional tools.",
		maxIterations,
	)
	a.reporter.ReportCapReached(maxIterations)

	messages = append(messages, llm.Message{Role: llm.RoleUser, Text: capMsg})
	a.reporter.ReportFinalReview()

	// Pass nil tools so the provider cannot issue further tool calls even if
	// it ignores the cap instruction in the prompt.
	resp, err := a.generateContentWithEmptyRetry(ctx, messages, nil, maxTokens)
	if err != nil {
		return "", fmt.Errorf("llm call failed on forced-final review: %w", err)
	}

	a.reporter.ReportUsage(resp.Usage)
	a.trackUsage(resp.Usage)

	if resp.Text == "" {
		return "", fmt.Errorf("llm returned empty content on forced-final review after %d attempts", emptyResponseMaxAttempts)
	}
	return resp.Text, nil
}

func (a *Agent) trackUsage(usage llm.Usage) {
	if usage.PromptTokens > 0 {
		a.totalUsage.PromptTokens += usage.PromptTokens
	}
	if usage.OutputTokens > 0 {
		a.totalUsage.OutputTokens += usage.OutputTokens
	}
	if usage.ThinkingTokens > 0 {
		a.totalUsage.ThinkingTokens += usage.ThinkingTokens
	}
	if usage.CachedTokens > 0 {
		a.totalUsage.CachedTokens += usage.CachedTokens
	}
}

// generateContentWithEmptyRetry calls GenerateContent and retries up to
// emptyResponseMaxAttempts times when the provider returns a nil error but
// empty content with no tool calls. This is a distinct failure mode from a
// hard error: the SDK call succeeds but the body is empty, which can happen
// transiently under provider load.
//
// If the response has tool calls (even with empty text) it is returned
// immediately — non-empty tool-call responses are always valid.
func (a *Agent) generateContentWithEmptyRetry(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, maxTokens int) (*llm.Response, error) {
	var lastResp *llm.Response
	for attempt := range emptyResponseMaxAttempts {
		resp, err := a.llm.GenerateContent(ctx, messages, tools, maxTokens)
		if err != nil {
			return nil, err
		}
		// A response with tool calls is always actionable, even if Text is empty.
		if len(resp.ToolCalls) > 0 || resp.Text != "" {
			return resp, nil
		}
		lastResp = resp
		if attempt < emptyResponseMaxAttempts-1 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			a.reporter.ReportEmptyResponseRetry(attempt + 2)
		}
	}
	return lastResp, nil
}

// compactToolCallArgs returns a short human-readable summary of tool arguments.
func compactToolCallArgs(tc llm.ToolCall) string {
	if tc.Arguments == "" {
		return "no args"
	}
	s := tc.Arguments
	const maxLen = 120
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}
