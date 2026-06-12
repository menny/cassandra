package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"golang.org/x/term"
)

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

// ConsoleFormatter abstracts formatting and styling for console output.
type ConsoleFormatter interface {
	NotifyUser() string
	FormatToolCalls(tcs []llm.ToolCall) string
	FormatReviewHeader(files int, guidelines, model string) string
	FormatConfig(cfg *config.Config, targetDir string) string
	FormatReview(result string) string
	StyleStderr(plain, styled string, color string, bold bool) string
}

// consoleReporter formats semantic messages and delegates rendering to a consoleWriter.
type consoleReporter struct {
	writer    consoleWriter
	formatter ConsoleFormatter
}

// NewRawReporter creates a reporter that prints raw text.
func NewRawReporter(stdout, stderr io.Writer) Reporter {
	return &consoleReporter{
		writer:    &rawWriter{stdout: stdout, stderr: stderr},
		formatter: rawFormatter{},
	}
}

// NewMarkdownReporter creates a reporter that renders markdown via glamour.
func NewMarkdownReporter(stdout, stderr io.Writer) Reporter {
	return &consoleReporter{
		writer:    &rawWriter{stdout: stdout, stderr: stderr},
		formatter: markdownFormatter{},
	}
}

// NewDefaultReporter creates a raw reporter for backward compatibility.
func NewDefaultReporter(w io.Writer) Reporter {
	return NewRawReporter(w, w)
}

func (r *consoleReporter) NotifyUser() {
	if bell := r.formatter.NotifyUser(); bell != "" {
		r.writer.WriteStderr(bell)
	}
}

func (r *consoleReporter) writeStyledStderr(plain, styled string, color string, bold bool) {
	r.writer.WriteStderr(r.formatter.StyleStderr(plain, styled, color, bold))
}

func (r *consoleReporter) ReportIteration(iter int) {
	r.writeStyledStderr(
		fmt.Sprintf("🔍 [Iter %d] Reviewing...", iter),
		fmt.Sprintf("🔍 [Iteration %d]", iter),
		"178",
		true,
	)
}

func (r *consoleReporter) ReportToolCalls(tcs []llm.ToolCall) {
	formatted := r.formatter.FormatToolCalls(tcs)
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
	r.writer.WriteStderr(r.formatter.FormatReviewHeader(files, guidelines, model))
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
	r.writer.WriteStderr(r.formatter.FormatConfig(cfg, targetDir))
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
	r.writer.WriteStdout(r.formatter.FormatReview(result))
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

// rawFormatter formats output in plain unstyled text.
type rawFormatter struct{}

func (rawFormatter) NotifyUser() string {
	return ""
}

func (rawFormatter) StyleStderr(plain, styled string, color string, bold bool) string {
	return plain + "\n"
}

func (rawFormatter) FormatReviewHeader(files int, guidelines string, model string) string {
	return fmt.Sprintf("\n✅ Review generated successfully.\n\n\n# 📝 Review for %d files using %s (%s)\n\n", files, guidelines, model)
}

func (rawFormatter) FormatConfig(cfg *config.Config, targetDir string) string {
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

	return "\n" + t.Render() + "\n\n"
}

func (rawFormatter) FormatReview(result string) string {
	return result + "\n"
}

func (rawFormatter) FormatToolCalls(tcs []llm.ToolCall) string {
	var sb strings.Builder
	for _, tc := range tcs {
		if tc.Name == "emit_reviewer_state" {
			var args struct {
				Message   string `json:"message"`
				FocusArea string `json:"focus_area"`
			}
			_ = json.Unmarshal([]byte(tc.Arguments), &args)

			if sb.Len() > 0 {
				sb.WriteString("\n")
			}

			reviewerStateTitle := "[Reviewer state]"
			if len(args.FocusArea) > 0 {
				fmt.Fprintf(&sb, "🧠 %s focus area: %s\n", reviewerStateTitle, args.FocusArea)
			} else {
				fmt.Fprintf(&sb, "🧠 %s\n", reviewerStateTitle)
			}
			lines := strings.Split(args.Message, "\n")
			for _, line := range lines {
				sb.WriteString("  " + line + "\n")
			}
			sb.WriteString("\n")
		} else {
			fmt.Fprintf(&sb, "* 🛠️  [Tool] %s(%s)\n", tc.Name, compactToolCallArgs(tc))
		}
	}
	return sb.String()
}

// markdownFormatter formats output using Lipgloss and Glamour styles.
type markdownFormatter struct{}

func (markdownFormatter) NotifyUser() string {
	return "\a"
}

func (markdownFormatter) StyleStderr(plain, styled string, color string, bold bool) string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if bold {
		style = style.Bold(true)
	}
	return style.Render(styled) + "\n"
}

func (markdownFormatter) FormatReviewHeader(files int, guidelines string, model string) string {
	success := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("108")).Render("✅ Review generated successfully.")
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("107")).Render(fmt.Sprintf("# 📝 Review for %d files using %s (%s)", files, guidelines, model))
	return fmt.Sprintf("\n%s\n\n\n%s\n\n", success, title)
}

func (markdownFormatter) FormatConfig(cfg *config.Config, targetDir string) string {
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

	return "\n" + t.Render() + "\n\n"
}

func (markdownFormatter) FormatReview(result string) string {
	return renderMarkdown(result, os.Stdout) + "\n"
}

func (markdownFormatter) FormatToolCalls(tcs []llm.ToolCall) string {
	var sb strings.Builder
	toolNameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("216")) // Warm Amber / Peach
	toolArgsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // Muted Stone Grey

	stateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // Terracotta / Warm Clay
	focusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("223")) // Soft Sand / Cream

	for _, tc := range tcs {
		if tc.Name == "emit_reviewer_state" {
			var args struct {
				Message   string `json:"message"`
				FocusArea string `json:"focus_area"`
			}
			_ = json.Unmarshal([]byte(tc.Arguments), &args)

			if sb.Len() > 0 {
				sb.WriteString("\n")
			}

			reviewerStateTitle := stateStyle.Render("[Reviewer state]")
			if len(args.FocusArea) > 0 {
				focusAreaTitle := focusStyle.Render(fmt.Sprintf("focus area: %s", args.FocusArea))
				fmt.Fprintf(&sb, "🧠 %s %s\n", reviewerStateTitle, focusAreaTitle)
			} else {
				fmt.Fprintf(&sb, "🧠 %s\n", reviewerStateTitle)
			}
			msg := renderMarkdown(args.Message, os.Stderr)
			messageStyle := lipgloss.NewStyle().MarginLeft(2).MarginBottom(1)
			sb.WriteString(messageStyle.Render(msg))
		} else {
			argsStr := compactToolCallArgs(tc)
			fmt.Fprintf(&sb, "* 🛠️  [Tool] %s(%s)\n", toolNameStyle.Render(tc.Name), toolArgsStyle.Render(argsStr))
		}
	}
	return sb.String()
}
