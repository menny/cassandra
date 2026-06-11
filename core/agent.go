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
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
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
	ReportToolStatus(name string, status string, err error)
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
	NotifyUser()
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

func getTerminalWidth(writer io.Writer) int {
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

type markdownRenderer struct {
	mu    sync.Mutex
	width int
	r     *glamour.TermRenderer
}

func (mr *markdownRenderer) Render(s string, writer io.Writer) string {
	width := getTerminalWidth(writer)
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if mr.r == nil || mr.width != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return s
		}
		mr.r = r
		mr.width = width
	}

	rendered, err := mr.r.Render(s)
	if err != nil {
		return s
	}
	rendered = strings.TrimPrefix(rendered, "\n")
	rendered = strings.TrimSuffix(rendered, "\n")
	return rendered
}

var mdRenderer = &markdownRenderer{}

func renderMarkdown(s string, writer io.Writer) string {
	return mdRenderer.Render(s, writer)
}

// consoleReporter formats semantic messages and delegates rendering to a consoleWriter.
type consoleReporter struct {
	writer              consoleWriter
	writeStyledStderrFn func(plain, styled string, color string, bold bool)
}

// NewRawReporter creates a reporter that prints raw text.
func NewRawReporter(stdout, stderr io.Writer) Reporter {
	return &consoleReporter{
		writer: &rawWriter{stdout: stdout, stderr: stderr},
	}
}

// markdownReport renders markdown via glamour and inherits from consoleReporter.
type markdownReport struct {
	consoleReporter
}

// NewMarkdownReporter creates a reporter that renders markdown via glamour.
func NewMarkdownReporter(stdout, stderr io.Writer) Reporter {
	mr := &markdownReport{
		consoleReporter: consoleReporter{
			writer: &rawWriter{stdout: stdout, stderr: stderr},
		},
	}
	mr.writeStyledStderrFn = mr.writeStyledStderrMarkdown
	return mr
}

// NewDefaultReporter creates a raw reporter for backward compatibility.
func NewDefaultReporter(w io.Writer) Reporter {
	return NewRawReporter(w, w)
}

func (r *consoleReporter) NotifyUser() {}

func (r *markdownReport) NotifyUser() {
	r.writer.WriteStderr("\a")
}

func (r *consoleReporter) writeStyledStderr(plain, styled string, color string, bold bool) {
	if r.writeStyledStderrFn != nil {
		r.writeStyledStderrFn(plain, styled, color, bold)
		return
	}
	r.writer.WriteStderr(plain + "\n")
}

func (r *markdownReport) writeStyledStderrMarkdown(plain, styled string, color string, bold bool) {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if bold {
		style = style.Bold(true)
	}
	r.writer.WriteStderr(style.Render(styled) + "\n")
}

func (r *consoleReporter) ReportIteration(iter int) {
	r.writeStyledStderr(
		fmt.Sprintf("🔍 [Iter %d] Reviewing...", iter),
		fmt.Sprintf("🔍 [Iteration %d]", iter),
		"178",
		true,
	)
}

func formatToolCalls(tcs []llm.ToolCall,
	formatStandard func(tc llm.ToolCall) string,
	formatReviewerState func(message, focusArea string) string,
) string {
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
			sb.WriteString(formatStandard(tc) + "\n")
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

		sb.WriteString(formatReviewerState(args.Message, args.FocusArea))
	}

	return sb.String()
}

func (r *consoleReporter) ReportToolCalls(tcs []llm.ToolCall) {
	formatted := formatToolCalls(tcs,
		func(tc llm.ToolCall) string {
			return fmt.Sprintf("* 🛠️  [Tool] %s(%s)", tc.Name, compactToolCallArgs(tc))
		},
		func(message, focusArea string) string {
			reviewerStateTitle := "[Reviewer state]"
			var focusAreaTitle string
			if len(focusArea) > 0 {
				focusAreaTitle = fmt.Sprintf("focus area: %s", focusArea)
			}

			var sb strings.Builder
			if len(focusArea) > 0 {
				fmt.Fprintf(&sb, "🧠 %s %s\n", reviewerStateTitle, focusAreaTitle)
			} else {
				fmt.Fprintf(&sb, "🧠 %s\n", reviewerStateTitle)
			}
			lines := strings.Split(message, "\n")
			for _, line := range lines {
				sb.WriteString("  " + line + "\n")
			}
			sb.WriteString("\n")
			return sb.String()
		},
	)

	if len(formatted) > 0 {
		r.writer.WriteStderr(formatted)
	}
}

func (r *markdownReport) ReportToolCalls(tcs []llm.ToolCall) {
	toolNameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("216")) // Warm Amber / Peach
	toolArgsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // Muted Stone Grey

	stateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // Terracotta / Warm Clay
	focusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("223")) // Soft Sand / Cream

	formatted := formatToolCalls(tcs,
		func(tc llm.ToolCall) string {
			argsStr := compactToolCallArgs(tc)
			return fmt.Sprintf("* 🛠️  [Tool] %s(%s)", toolNameStyle.Render(tc.Name), toolArgsStyle.Render(argsStr))
		},
		func(message, focusArea string) string {
			reviewerStateTitle := stateStyle.Render("[Reviewer state]")
			var focusAreaTitle string
			if len(focusArea) > 0 {
				focusAreaTitle = focusStyle.Render(fmt.Sprintf("focus area: %s", focusArea))
			}

			var sb strings.Builder
			if len(focusArea) > 0 {
				fmt.Fprintf(&sb, "🧠 %s %s\n", reviewerStateTitle, focusAreaTitle)
			} else {
				fmt.Fprintf(&sb, "🧠 %s\n", reviewerStateTitle)
			}
			msg := renderMarkdown(message, os.Stderr)
			messageStyle := lipgloss.NewStyle().MarginLeft(2).MarginBottom(1)
			sb.WriteString(messageStyle.Render(msg))
			return sb.String()
		},
	)

	if len(formatted) > 0 {
		r.writer.WriteStderr(formatted)
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
	r.writeStyledStderr(
		"📝 Formulating final review...",
		"📝 Formulating final review...",
		"178",
		true,
	)
}

func (r *consoleReporter) ReportExtraction() {
	r.writeStyledStderr(
		"📦 Extracting findings...",
		"📦 Extracting findings...",
		"178",
		true,
	)
}

func (r *consoleReporter) ReportExtractionRetry(attempt int) {
	msg := fmt.Sprintf("🔄 [Retry] Extraction attempt %d failed; retrying...", attempt)
	r.writeStyledStderr(msg, msg, "208", true)
}

func (r *consoleReporter) ReportEmptyResponseRetry(attempt int) {
	msg := fmt.Sprintf("🔄 [Retry] LLM returned empty response (attempt %d); retrying...", attempt)
	r.writeStyledStderr(msg, msg, "208", true)
}

func (r *consoleReporter) ReportCapReached(maxIterations int) {
	msg := fmt.Sprintf("⚠️  Reached maximum ReAct iterations (%d). Forcing final review.", maxIterations)
	r.writeStyledStderr(msg, msg, "208", true)
}

func (r *consoleReporter) ReportTruncated(maxTokens int) {
	msg := fmt.Sprintf("⚠️  LLM response truncated (hit max-tokens limit of %d). The review may be incomplete.", maxTokens)
	r.writeStyledStderr(msg, msg, "208", true)
}

func (r *consoleReporter) ReportMCPStatus(name string, status string, err error) {
	if err != nil {
		r.writer.WriteStderr(fmt.Sprintf("🔌 [MCP] %s: %s: %v\n", name, status, err))
	} else {
		r.writer.WriteStderr(fmt.Sprintf("🔌 [MCP] %s: %s\n", name, status))
	}
}

func (r *consoleReporter) ReportToolStatus(name string, status string, err error) {
	// Console reporter does not display individual tool execution logs to avoid terminal clutter.
	// In-place updates are managed by the TUI reporter.
}

func (r *consoleReporter) ReportReviewHeader(files int, guidelines string, model string) {
	r.writer.WriteStderr(fmt.Sprintf("\n✅ Review generated successfully.\n\n\n# 📝 Review for %d files using %s (%s)\n\n", files, guidelines, model))
}

func (r *markdownReport) ReportReviewHeader(files int, guidelines string, model string) {
	success := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("108")).Render("✅ Review generated successfully.")
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("107")).Render(fmt.Sprintf("# 📝 Review for %d files using %s (%s)", files, guidelines, model))
	r.writer.WriteStderr(fmt.Sprintf("\n%s\n\n\n%s\n\n", success, title))
}

func buildConfigTable(cfg *config.Config, targetDir string) *table.Table {
	t := table.New().Headers("Configuration", "Value")
	t.Row("Working Directory", targetDir)
	t.Row("Base", cfg.Base)
	t.Row("Head", cfg.Head)
	t.Row("LLM Provider", cfg.Provider)
	t.Row("LLM Model", cfg.Model)
	if cfg.ProviderURL != "" {
		t.Row("LLM Provider URL", cfg.ProviderURL)
	}
	t.Row("Max Tokens", fmt.Sprintf("%d", cfg.MaxTokens))
	if len(cfg.ProviderOptions) > 0 {
		t.Row("Provider Options", fmt.Sprintf("%+v", cfg.ProviderOptions))
	}
	if cfg.MainGuidelines != "" {
		t.Row("Main Guidelines", cfg.MainGuidelines)
	}
	if len(cfg.SupplementalGuidelines) > 0 {
		for i, sg := range cfg.SupplementalGuidelines {
			t.Row(fmt.Sprintf("Supplemental Guidelines %d", i+1), sg)
		}
	}
	if cfg.WishlistDir != "" {
		t.Row("Wishlist Directory", cfg.WishlistDir)
	}
	if cfg.OutputJSONFile != "" {
		t.Row("Structured Output JSON", cfg.OutputJSONFile)
		if cfg.ExtractionModel != "" {
			t.Row("Extraction Model", cfg.ExtractionModel)
		}
	}
	if cfg.MetricsJSONFile != "" {
		t.Row("Session Metrics JSON", cfg.MetricsJSONFile)
	}
	if cfg.MetadataJSONFile != "" {
		t.Row("Metadata JSON", cfg.MetadataJSONFile)
	}
	if cfg.ApprovalEvaluationPromptFile != "" {
		t.Row("Approval Evaluation Prompt File", cfg.ApprovalEvaluationPromptFile)
	}
	t.Row("API Key", "[PROVIDED]")
	return t
}

func (r *consoleReporter) ReportConfig(cfg *config.Config, targetDir string) {
	t := buildConfigTable(cfg, targetDir)

	headerColor := "205"
	keyColor := "81"
	borderColor := "240"

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(headerColor)).
		Align(lipgloss.Left).
		Padding(0, 1)

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(keyColor)).
		Padding(0, 1)

	valueStyle := lipgloss.NewStyle().
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(borderColor))

	t.Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			if col == 0 {
				return keyStyle
			}
			return valueStyle
		})

	r.writer.WriteStderr("\n" + t.Render() + "\n\n")
}

func (r *markdownReport) ReportConfig(cfg *config.Config, targetDir string) {
	t := buildConfigTable(cfg, targetDir)

	headerColor := "107" // Laurel Green
	keyColor := "179"    // Warm Ochre
	borderColor := "241" // Dusty/Stone Grey

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(headerColor)).
		Align(lipgloss.Left).
		Padding(0, 1)

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(keyColor)).
		Padding(0, 1)

	valueStyle := lipgloss.NewStyle().
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(borderColor))

	t.Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			if col == 0 {
				return keyStyle
			}
			return valueStyle
		})

	r.writer.WriteStderr("\n" + t.Render() + "\n\n")
}

func (r *consoleReporter) ReportFetchingDiff() {
	r.writeStyledStderr(
		"🌿 Fetching git diff...",
		"🌿 Fetching git diff...",
		"108",
		false,
	)
}

func (r *consoleReporter) ReportFetchingCommits() {
	r.writeStyledStderr(
		"🌿 Fetching git commits...",
		"🌿 Fetching git commits...",
		"108",
		false,
	)
}

func (r *consoleReporter) ReportNoChanges() {
	r.writeStyledStderr(
		"⚪ No changes found.",
		"⚪ No changes found.",
		"243",
		false,
	)
}

func (r *consoleReporter) ReportReview(result string) error {
	r.writer.WriteStdout(result + "\n")
	return nil
}

func (r *markdownReport) ReportReview(result string) error {
	r.writer.WriteStdout(renderMarkdown(result, os.Stdout) + "\n")
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
	var warnStr string
	if err != nil {
		warnStr = fmt.Sprintf("⚠️  %s: %v", msg, err)
	} else {
		warnStr = fmt.Sprintf("⚠️  %s", msg)
	}
	r.writeStyledStderr(warnStr, warnStr, "208", true)
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

	for _, tc := range toolCalls {
		a.toolCalls[tc.Name]++
		a.reporter.ReportToolStatus(tc.Name, "started", nil)
	}

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(i int, tc llm.ToolCall) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				a.reporter.ReportToolStatus(tc.Name, "failed", ctx.Err())
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
				a.reporter.ReportToolStatus(tc.Name, "failed", err)
				toolMsg.ToolResults[i] = llm.ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    fmt.Sprintf("error: %v", err),
				}
				return
			}

			defer func() {
				if r := recover(); r != nil {
					err := fmt.Errorf("tool panicked: %v", r)
					a.reporter.ReportToolStatus(tc.Name, "failed", err)
					toolMsg.ToolResults[i] = llm.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Content:    fmt.Sprintf("error: %v", err),
					}
				}
			}()

			// Dispatch; on error, surface the message as the tool result so the
			// LLM can reason about it rather than crashing the whole loop.
			result, toolErr := a.registry.HandleCall(ctx, tc)
			if toolErr != nil {
				a.reporter.ReportToolStatus(tc.Name, "failed", toolErr)
				result = fmt.Sprintf("error: %v", toolErr)
			} else {
				a.reporter.ReportToolStatus(tc.Name, "completed", nil)
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
