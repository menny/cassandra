package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
	"golang.org/x/term"
)

const (
	// MaxIterationsPerFile is the recommended number of ReAct loop iterations to
	// allocate per changed file in the diff.
	MaxIterationsPerFile = 5
	// AbsoluteMaxIter is the hard upper bound for the ReAct loop to prevent
	// infinite recursion or excessive token spend.
	AbsoluteMaxIter = 25
	// MaxToolConcurrency is the maximum number of tools allowed to run
	// in parallel in a single turn.
	MaxToolConcurrency = 8
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
	HandleCall(ctx context.Context, tc llm.ToolCall) (string, error)
}

// Reporter defines how the Agent reports progress and diagnostics.
type Reporter interface {
	ReportIteration(iter int)
	ReportToolCalls(tcs []llm.ToolCall)
	ReportUsage(usage llm.Usage)
	ReportUsageSummary(usage llm.Usage)
	ReportFinalReview()
	ReportExtraction()
	ReportExtractionRetry(attempt int)
	ReportEmptyResponseRetry(attempt int)
	ReportCapReached(maxIterations int)
	ReportTruncated(maxTokens int)
	ReportMCPStatus(name string, status string, err error)
	ReportReviewHeader(files int, guidelines string, model string)

	// Additional lifecycle methods
	ReportConfig(cfg *config.Config, targetDir string)
	ReportFetchingDiff()
	ReportFetchingCommits()
	ReportNoChanges()
	ReportReview(result string) error
	ReportReviewWritten(file string)
	ReportStructuredReviewWritten(file string)
	ReportMetricsWritten(file string)
	ReportWarning(msg string, err error)
	ReportError(err error)
}

// consoleWriter defines how formatted strings are printed.
type consoleWriter interface {
	WriteStdout(s string)
	WriteStderr(s string)
}

// rawWriter writes strings directly to stdout/stderr.
type rawWriter struct {
	stdout io.Writer
	stderr io.Writer
}

func (w *rawWriter) WriteStdout(s string) {
	fmt.Fprint(w.stdout, s)
}

func (w *rawWriter) WriteStderr(s string) {
	fmt.Fprint(w.stderr, s)
}

// glamourWriter renders strings using Glamour before printing.
type glamourWriter struct {
	stdout io.Writer
	stderr io.Writer
}

func (w *glamourWriter) getWidth(writer io.Writer) int {
	if f, ok := writer.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	if width, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && width > 0 {
		return width
	}
	return 0
}

func (w *glamourWriter) WriteStdout(s string) {
	width := w.getWidth(w.stdout)
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		fmt.Fprint(w.stdout, s)
		return
	}
	rendered, err := r.Render(s)
	if err != nil {
		fmt.Fprint(w.stdout, s)
		return
	}
	rendered = strings.TrimPrefix(rendered, "\n")
	rendered = strings.TrimSuffix(rendered, "\n")
	fmt.Fprint(w.stdout, rendered)
}

func (w *glamourWriter) WriteStderr(s string) {
	width := w.getWidth(w.stderr)
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		fmt.Fprint(w.stderr, s)
		return
	}
	rendered, err := r.Render(s)
	if err != nil {
		fmt.Fprint(w.stderr, s)
		return
	}
	rendered = strings.TrimPrefix(rendered, "\n")
	rendered = strings.TrimSuffix(rendered, "\n")
	fmt.Fprint(w.stderr, rendered)
}

// consoleReporter formats semantic messages and delegates rendering to a consoleWriter.
type consoleReporter struct {
	writer consoleWriter
}

// NewRawReporter creates a reporter that prints raw text.
func NewRawReporter(stdout, stderr io.Writer) Reporter {
	return &consoleReporter{
		writer: &rawWriter{stdout: stdout, stderr: stderr},
	}
}

// NewMarkdownReporter creates a reporter that renders markdown via glamour.
func NewMarkdownReporter(stdout, stderr io.Writer) Reporter {
	return &consoleReporter{
		writer: &glamourWriter{stdout: stdout, stderr: stderr},
	}
}

// NewDefaultReporter creates a raw reporter for backward compatibility.
func NewDefaultReporter(w io.Writer) Reporter {
	return NewRawReporter(w, w)
}

func (r *consoleReporter) ReportIteration(iter int) {
	r.writer.WriteStderr(fmt.Sprintf("🔍 [Iter %d] Reviewing...\n", iter))
}

func (r *consoleReporter) ReportToolCalls(tcs []llm.ToolCall) {
	var standardCalls []llm.ToolCall
	var emitReviewerStates []llm.ToolCall

	for _, tc := range tcs {
		if tc.Name == "emit_reviewer_state" {
			emitReviewerStates = append(emitReviewerStates, tc)
		} else {
			standardCalls = append(standardCalls, tc)
		}
	}

	var sb strings.Builder

	if len(standardCalls) > 0 {
		for _, tc := range standardCalls {
			fmt.Fprintf(&sb, "* 🛠️  [Tool] %s(%s)\n", tc.Name, compactToolCallArgs(tc))
		}
	}

	for _, tc := range emitReviewerStates {
		var args struct {
			Message   string `json:"message"`
			FocusArea string `json:"focus_area"`
		}
		_ = json.Unmarshal([]byte(tc.Arguments), &args)

		if sb.Len() > 0 {
			sb.WriteString("\n")
		}

		fmt.Fprintf(&sb, "[Reviewer state] focus area: %s\n", args.FocusArea)
		fmt.Fprintf(&sb, "%s\n", args.Message)
	}

	if sb.Len() > 0 {
		r.writer.WriteStderr(sb.String())
	}
}

func (r *consoleReporter) ReportUsage(usage llm.Usage) {
	if usage.PromptTokens >= 0 && usage.OutputTokens >= 0 {
		msg := fmt.Sprintf("📊 %d in, %d out", usage.TotalInput(), usage.TotalOutput())
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
		r.writer.WriteStderr(msg + "\n")
	}
}

func (r *consoleReporter) ReportUsageSummary(total llm.Usage) {
	if total.PromptTokens > 0 || total.OutputTokens > 0 {
		r.writer.WriteStderr(fmt.Sprintf("📈 %d in, %d out (total)\n", total.TotalInput(), total.TotalOutput()))
	}
}

func (r *consoleReporter) ReportFinalReview() {
	r.writer.WriteStderr("📝 Formulating final review...\n")
}

func (r *consoleReporter) ReportExtraction() {
	r.writer.WriteStderr("📦 Extracting findings...\n")
}

func (r *consoleReporter) ReportExtractionRetry(attempt int) {
	r.writer.WriteStderr(fmt.Sprintf("🔄 [Retry] Extraction attempt %d failed; retrying...\n", attempt))
}

func (r *consoleReporter) ReportEmptyResponseRetry(attempt int) {
	r.writer.WriteStderr(fmt.Sprintf("🔄 [Retry] LLM returned empty response (attempt %d); retrying...\n", attempt))
}

func (r *consoleReporter) ReportCapReached(maxIterations int) {
	r.writer.WriteStderr(fmt.Sprintf("⚠️  Reached maximum ReAct iterations (%d). Forcing final review.\n", maxIterations))
}

func (r *consoleReporter) ReportTruncated(maxTokens int) {
	r.writer.WriteStderr(fmt.Sprintf("⚠️  LLM response truncated (hit max-tokens limit of %d). The review may be incomplete.\n", maxTokens))
}

func (r *consoleReporter) ReportMCPStatus(name string, status string, err error) {
	if err != nil {
		r.writer.WriteStderr(fmt.Sprintf("🔌 [MCP] %s: %s: %v\n", name, status, err))
	} else {
		r.writer.WriteStderr(fmt.Sprintf("🔌 [MCP] %s: %s\n", name, status))
	}
}

func (r *consoleReporter) ReportReviewHeader(files int, guidelines string, model string) {
	r.writer.WriteStderr(fmt.Sprintf("\n✅ Review generated successfully.\n\n\n# 📝 Review for %d files using %s (%s)\n\n", files, guidelines, model))
}

func (r *consoleReporter) ReportConfig(cfg *config.Config, targetDir string) {
	var sb strings.Builder
	sb.WriteString("| Configuration | Value |\n")
	sb.WriteString("| --- | --- |\n")
	fmt.Fprintf(&sb, "| **Working Directory** | %s |\n", targetDir)
	fmt.Fprintf(&sb, "| **Base** | %s |\n", cfg.Base)
	fmt.Fprintf(&sb, "| **Head** | %s |\n", cfg.Head)
	fmt.Fprintf(&sb, "| **LLM Provider** | %s |\n", cfg.Provider)
	fmt.Fprintf(&sb, "| **LLM Model** | %s |\n", cfg.Model)
	if cfg.ProviderURL != "" {
		fmt.Fprintf(&sb, "| **LLM Provider URL** | %s |\n", cfg.ProviderURL)
	}
	fmt.Fprintf(&sb, "| **Max Tokens** | %d |\n", cfg.MaxTokens)
	if len(cfg.ProviderOptions) > 0 {
		fmt.Fprintf(&sb, "| **Provider Options** | %+v |\n", cfg.ProviderOptions)
	}
	if cfg.MainGuidelines != "" {
		fmt.Fprintf(&sb, "| **Main Guidelines** | %s |\n", cfg.MainGuidelines)
	}
	if len(cfg.SupplementalGuidelines) > 0 {
		for sIndex, sg := range cfg.SupplementalGuidelines {
			fmt.Fprintf(&sb, "| **Supplemental Guidelines %d:** | - %s |\n", sIndex+1, sg)
		}
	}
	if cfg.WishlistDir != "" {
		fmt.Fprintf(&sb, "| **Wishlist Directory** | %s |\n", cfg.WishlistDir)
	}
	if cfg.OutputJSONFile != "" {
		fmt.Fprintf(&sb, "| **Structured Output JSON** | %s |\n", cfg.OutputJSONFile)
		if cfg.ExtractionModel != "" {
			fmt.Fprintf(&sb, "| **Extraction Model** | %s |\n", cfg.ExtractionModel)
		}
	}
	if cfg.MetricsJSONFile != "" {
		fmt.Fprintf(&sb, "| **Session Metrics JSON** | %s |\n", cfg.MetricsJSONFile)
	}
	if cfg.MetadataJSONFile != "" {
		fmt.Fprintf(&sb, "| **Metadata JSON** | %s |\n", cfg.MetadataJSONFile)
	}
	if cfg.ApprovalEvaluationPromptFile != "" {
		fmt.Fprintf(&sb, "| **Approval Evaluation Prompt File** | %s |\n", cfg.ApprovalEvaluationPromptFile)
	}
	sb.WriteString("| **API Key** | **[PROVIDED]** |\n")

	sb.WriteString("\n\n")

	r.writer.WriteStderr(sb.String())
}

func (r *consoleReporter) ReportFetchingDiff() {
	r.writer.WriteStderr("🌿 Fetching git diff...\n")
}

func (r *consoleReporter) ReportFetchingCommits() {
	r.writer.WriteStderr("🌿 Fetching git commits...\n")
}

func (r *consoleReporter) ReportNoChanges() {
	r.writer.WriteStderr("⚪ No changes found.\n")
}

func (r *consoleReporter) ReportReview(result string) error {
	r.writer.WriteStdout(result + "\n")
	return nil
}

func (r *consoleReporter) ReportReviewWritten(file string) {
	r.writer.WriteStderr(fmt.Sprintf("📝 Review written to %s\n", file))
}

func (r *consoleReporter) ReportStructuredReviewWritten(file string) {
	r.writer.WriteStderr(fmt.Sprintf("📦 Structured review written to %s\n", file))
}

func (r *consoleReporter) ReportMetricsWritten(file string) {
	r.writer.WriteStderr(fmt.Sprintf("📈 Metrics written to %s\n", file))
}

func (r *consoleReporter) ReportWarning(msg string, err error) {
	if err != nil {
		r.writer.WriteStderr(fmt.Sprintf("⚠️  %s: %v\n", msg, err))
	} else {
		r.writer.WriteStderr(fmt.Sprintf("⚠️  %s\n", msg))
	}
}

func (r *consoleReporter) ReportError(err error) {
	r.writer.WriteStderr(fmt.Sprintf("Error: %v\n", err))
}

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// WithStderr redirects diagnostic/progress output to w instead of os.Stderr.
// Useful in tests to suppress noise (pass io.Discard).
func WithStderr(w io.Writer) AgentOption {
	return func(a *Agent) {
		a.reporter = NewRawReporter(io.Discard, w)
		a.stderr = w
	}
}

// WithReporter sets a custom reporter for the Agent.
func WithReporter(r Reporter) AgentOption {
	return func(a *Agent) {
		if r != nil {
			a.reporter = r
		}
	}
}

// Agent orchestrates the ReAct (Reason + Act) loop between the LLM and the tool registry.
type Agent struct {
	llm        llm.Model
	registry   ToolDispatcher
	reporter   Reporter
	totalUsage llm.Usage
	toolCalls  map[string]int
	iterations int
	stderr     io.Writer
}

// NewAgent creates an Agent. Diagnostic / progress output goes to os.Stderr by
// default; override with WithStderr or WithReporter. The final review is
// returned as a string (caller routes it to stdout).
func NewAgent(model llm.Model, registry ToolDispatcher, opts ...AgentOption) *Agent {
	a := &Agent{
		llm:       model,
		registry:  registry,
		reporter:  NewDefaultReporter(os.Stderr),
		toolCalls: make(map[string]int),
		stderr:    os.Stderr,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Reporter returns the Agent's reporter.
func (a *Agent) Reporter() Reporter {
	return a.reporter
}

// GetMetrics returns the collected usage and execution metrics for the session.
func (a *Agent) GetMetrics() SessionMetrics {
	toolCallsCopy := make(map[string]int, len(a.toolCalls))
	totalTools := 0
	for k, v := range a.toolCalls {
		toolCallsCopy[k] = v
		totalTools += v
	}

	return SessionMetrics{
		Tokens: TokenMetrics{
			Input:       a.totalUsage.PromptTokens,
			Output:      a.totalUsage.OutputTokens,
			Thinking:    a.totalUsage.ThinkingTokens,
			Cached:      a.totalUsage.CachedTokens,
			TotalInput:  a.totalUsage.TotalInput(),
			TotalOutput: a.totalUsage.TotalOutput(),
		},
		Iterations: a.iterations,
		ToolCalls: ToolCallMetrics{
			Total:  totalTools,
			ByTool: toolCallsCopy,
		},
	}
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
	ctx = tools.ContextWithStateWriter(ctx, a.stderr)
	if maxIterations <= 0 {
		maxIterations = AbsoluteMaxIter
	}
	if maxTokens <= 0 {
		maxTokens = llm.DefaultMaxTokens
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Text: stableSystem, CacheBreakpoint: true},
	}
	if dynamicSystem != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Text: dynamicSystem})
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Text: requestText})

	tools := a.registry.ToTools()

	defer func() {
		a.reporter.ReportUsageSummary(a.totalUsage)
	}()

	for iter := range maxIterations {
		a.iterations = iter + 1
		a.reporter.ReportIteration(a.iterations)

		resp, err := a.generateContentWithEmptyRetry(ctx, messages, tools, maxTokens)
		if err != nil {
			return "", fmt.Errorf("llm call failed on iteration %d: %w", iter+1, err)
		}

		a.reporter.ReportUsage(resp.Usage)
		a.totalUsage.Add(resp.Usage)

		if resp.FinishReason == llm.FinishReasonLength {
			a.reporter.ReportTruncated(maxTokens)
		}

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

		toolMsg := a.executeToolCalls(ctx, resp.ToolCalls)
		// executeToolCalls returns a ToolResults slice with len(toolCalls) elements.
		// Since len(resp.ToolCalls) > 0, len(toolMsg.ToolResults) is guaranteed to be > 0.
		// The guard is a defensive check to prevent out-of-bounds access.
		if len(toolMsg.ToolResults) > 0 {
			remaining := maxIterations - (iter + 1)
			// Only append budget notes if there is at least one remaining tool iteration.
			// When remaining == 0, the next turn will hit the end of the loop and trigger
			// handleCapReached unconditionally, which appends its own forced-final cap message.
			// Thus, a remaining == 0 budget note is intentionally omitted to avoid redundant notes.
			if remaining > 0 {
				lastIdx := len(toolMsg.ToolResults) - 1
				var note string
				if remaining == 1 {
					note = fmt.Sprintf(budgetNoteLastTurn, iter+1, maxIterations)
				} else {
					note = fmt.Sprintf(budgetNoteGeneral, iter+1, maxIterations, remaining)
				}
				toolMsg.ToolResults[lastIdx].Content += note
			}
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

const budgetNoteLastTurn = "\n\n---\n[SYSTEM NOTE] Iteration %d of %d. 1 more turn left. This is your last turn to call tools! In the next turn, you will be forced to finalize your review without tools. Formulate your final review now if you have enough information."

const budgetNoteGeneral = "\n\n---\n[SYSTEM NOTE] Iteration %d of %d. Budget remaining: %d turns.\nPlease minimize iterations: only request further tool calls if needed to resolve remaining information gaps. If you already have enough context, formulate and return your final review now."

// ExtractStructuredReview takes a raw markdown review and converts it into a
// machine-readable StructuredReview using a second LLM pass.
// Hard LLM errors are returned immediately (the underlying RetryingModel has
// already exhausted its own retry budget for them). Only soft application-level
// failures — empty content and malformed JSON — are retried here, up to
// extractionMaxAttempts times total, to overcome non-determinism in LLM output.
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
			if err := sleepWithContext(ctx, llm.DefaultRetryBaseDelay); err != nil {
				return nil, err
			}
		}

		resp, err := a.llm.GenerateStructuredContent(ctx, messages, StructuredReviewSchema, config)
		if err != nil {
			return nil, fmt.Errorf("extraction failed: %w", err)
		}

		a.reporter.ReportUsage(resp.Usage)
		a.totalUsage.Add(resp.Usage)

		_, effectiveMaxTokens := config.Resolve("")
		if resp.FinishReason == llm.FinishReasonLength {
			a.reporter.ReportTruncated(effectiveMaxTokens)
		}

		if resp.Text == "" {
			lastErr = errors.New("extraction returned empty content")
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

func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []llm.ToolCall) llm.Message {
	// Execute all tool calls and collect results into ONE RoleTool message.
	// All ToolResults must be in a single message so providers see strict
	// role alternation (no consecutive same-role turns).
	toolMsg := llm.Message{
		Role:        llm.RoleTool,
		ToolResults: make([]llm.ToolResult, len(toolCalls)),
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxToolConcurrency)

	a.reporter.ReportToolCalls(toolCalls)

	for i, tc := range toolCalls {
		a.toolCalls[tc.Name]++

		wg.Add(1)
		go func(i int, tc llm.ToolCall) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				toolMsg.ToolResults[i] = llm.ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    fmt.Sprintf("error: %v", ctx.Err()),
				}
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			if err := ctx.Err(); err != nil {
				toolMsg.ToolResults[i] = llm.ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    fmt.Sprintf("error: %v", err),
				}
				return
			}

			defer func() {
				if r := recover(); r != nil {
					toolMsg.ToolResults[i] = llm.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Content:    fmt.Sprintf("error: tool panicked: %v", r),
					}
				}
			}()

			// Dispatch; on error, surface the message as the tool result so the
			// LLM can reason about it rather than crashing the whole loop.
			result, toolErr := a.registry.HandleCall(ctx, tc)
			if toolErr != nil {
				result = fmt.Sprintf("error: %v", toolErr)
			}

			toolMsg.ToolResults[i] = llm.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    result,
			}
		}(i, tc)
	}

	wg.Wait()
	return toolMsg
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
	a.totalUsage.Add(resp.Usage)

	if resp.Text == "" {
		return "", fmt.Errorf("llm returned empty content on forced-final review after %d attempts", emptyResponseMaxAttempts)
	}
	return resp.Text, nil
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
			if err := sleepWithContext(ctx, llm.DefaultRetryBaseDelay); err != nil {
				return nil, err
			}
		}
	}
	return lastResp, nil
}

// sleepWithContext blocks for d or until ctx is cancelled, whichever comes
// first. It returns ctx.Err() on cancellation and nil after the full sleep.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
