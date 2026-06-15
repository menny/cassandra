package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
)

// TUI message types
type mcpStatusMsg struct {
	name   string
	status string
	err    error
}

type toolStatusMsg struct {
	name   string
	status string
	err    error
}

type toolCallsMsg struct {
	tcs []llm.ToolCall
}

type iterationMsg struct {
	iter int
}

type llmStatusMsg struct {
	status     string
	llmWaiting bool
}

type warningMsg struct {
	warning string
}

type quitMsg struct{}

// TUI state representation
type mcpServerState struct {
	name   string
	status string
	err    error
}

type toolCallState struct {
	name      string
	arguments string
	status    string // "queued", "started", "completed", "failed"
	err       error
}

type reviewerState struct {
	focusArea       string
	message         string
	renderedMessage string
}

type iterationState struct {
	iter          int
	llmStatus     string
	llmWaiting    bool
	reviewerState *reviewerState
	toolCalls     []*toolCallState
}

type tuiModel struct {
	mcpServers  map[string]*mcpServerState
	mcpList     []string
	iterations  []*iterationState
	warnings    []string
	quitting    bool
	spinner     spinner.Model
	viewport    viewport.Model
	ready       bool
	configText  string
	userAborted bool
}

func (m *tuiModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	autoScroll := false

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		headerHeight := 3
		footerHeight := 2
		verticalMargin := headerHeight + footerHeight

		width := msg.Width
		if width < 1 {
			width = 1
		}
		height := msg.Height - verticalMargin
		if height < 1 {
			height = 1
		}

		if !m.ready {
			m.viewport = viewport.New(width, height)
			m.ready = true
		} else {
			m.viewport.Width = width
			m.viewport.Height = height
		}
		autoScroll = true

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			m.userAborted = true
			return m, tea.Quit
		}
		m.viewport, cmd = m.viewport.Update(msg)

	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)

	case mcpStatusMsg:
		state, exists := m.mcpServers[msg.name]
		if !exists {
			state = &mcpServerState{name: msg.name}
			m.mcpServers[msg.name] = state
			m.mcpList = append(m.mcpList, msg.name)
		}
		state.status = msg.status
		state.err = msg.err
		autoScroll = true

	case toolCallsMsg:
		if len(m.iterations) > 0 {
			current := m.iterations[len(m.iterations)-1]
			current.llmWaiting = false

			var standardCalls []*toolCallState
			var revState *reviewerState

			for _, tc := range msg.tcs {
				if tc.Name == "emit_reviewer_state" {
					var args struct {
						Message   string `json:"message"`
						FocusArea string `json:"focus_area"`
					}
					_ = json.Unmarshal([]byte(tc.Arguments), &args)
					revState = &reviewerState{
						focusArea:       args.FocusArea,
						message:         args.Message,
						renderedMessage: renderMarkdown(args.Message, os.Stderr),
					}
				} else {
					standardCalls = append(standardCalls, &toolCallState{
						name:      tc.Name,
						arguments: tc.Arguments,
						status:    "queued",
					})
				}
			}

			current.reviewerState = revState
			current.toolCalls = standardCalls

			if len(standardCalls) > 0 {
				current.llmStatus = "Executing tool calls..."
			} else {
				current.llmStatus = "LLM turn completed."
			}
		}
		autoScroll = true

	case toolStatusMsg:
		if len(m.iterations) > 0 {
			current := m.iterations[len(m.iterations)-1]
			if msg.status == "started" {
				for _, tc := range current.toolCalls {
					if tc.name == msg.name && tc.status == "queued" {
						tc.status = "started"
						break
					}
				}
			} else if msg.status == "completed" || msg.status == "failed" {
				for _, tc := range current.toolCalls {
					if tc.name == msg.name && tc.status == "started" {
						tc.status = msg.status
						tc.err = msg.err
						break
					}
				}
			}
		}
		autoScroll = true

	case iterationMsg:
		m.iterations = append(m.iterations, &iterationState{
			iter:       msg.iter,
			llmStatus:  "Waiting for LLM reply...",
			llmWaiting: true,
		})
		autoScroll = true

	case llmStatusMsg:
		if len(m.iterations) > 0 {
			current := m.iterations[len(m.iterations)-1]
			current.llmStatus = msg.status
			current.llmWaiting = msg.llmWaiting
		}
		autoScroll = true

	case warningMsg:
		m.warnings = append(m.warnings, msg.warning)
		autoScroll = true

	case quitMsg:
		m.quitting = true
		return m, tea.Quit
	}

	if m.ready {
		m.viewport.SetContent(m.renderContent())
		if autoScroll {
			m.viewport.GotoBottom()
		}
	}
	return m, cmd
}

func (m *tuiModel) View() string {
	if m.quitting {
		return ""
	}

	if !m.ready {
		return "Initializing...\n"
	}

	var sb strings.Builder

	// Static Header
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("107")).Render("🛸 Cassandra AI Reviewer") + "\n\n")

	// Viewport Content
	sb.WriteString(m.viewport.View() + "\n")

	// Static Footer
	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	sb.WriteString("\n" + footerStyle.Render("↑/↓: scroll • PgUp/PgDn: scroll page • Ctrl+C: abort"))

	return sb.String()
}

func (m *tuiModel) renderContent() string {
	var sb strings.Builder

	if m.configText != "" {
		sb.WriteString(m.configText)
	}

	// Section 1: MCP Servers
	if len(m.mcpList) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Render("🔌 MCP Servers:") + "\n")
		for _, name := range m.mcpList {
			state := m.mcpServers[name]
			var statusStr string
			switch state.status {
			case "started":
				statusStr = fmt.Sprintf("  %s %s: starting...", m.spinner.View(), name)
			case "loaded":
				statusStr = fmt.Sprintf("  %s: loaded", name)
			case "failed to load":
				statusStr = fmt.Sprintf("  %s: failed to load: %v ⚠️", name, state.err)
			default:
				statusStr = fmt.Sprintf("  %s: %s", name, state.status)
			}
			sb.WriteString(statusStr + "\n")
		}
		sb.WriteString("\n")
	}

	// Section 2: LLM Loop Progress (Iteration Blocks)
	for _, it := range m.iterations {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("178")).Render(fmt.Sprintf("🔍 [Iteration %d]", it.iter)) + "\n")

		if it.llmWaiting {
			sb.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), it.llmStatus))
		} else if it.reviewerState == nil && len(it.toolCalls) == 0 {
			sb.WriteString(fmt.Sprintf("  %s\n", it.llmStatus))
		}

		// Focus area / Reviewer state messages
		if it.reviewerState != nil {
			stateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // Terracotta / Warm Clay
			focusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("223")) // Soft Sand / Cream
			reviewerStateTitle := stateStyle.Render("[Reviewer state]")

			if len(it.reviewerState.focusArea) > 0 {
				focusAreaTitle := focusStyle.Render(fmt.Sprintf("focus area: %s", it.reviewerState.focusArea))
				sb.WriteString(fmt.Sprintf("  🧠 %s %s\n", reviewerStateTitle, focusAreaTitle))
			} else {
				sb.WriteString(fmt.Sprintf("  🧠 %s\n", reviewerStateTitle))
			}
			if len(it.reviewerState.renderedMessage) > 0 {
				msgStyle := lipgloss.NewStyle().MarginLeft(4).MarginBottom(1)
				sb.WriteString(msgStyle.Render(it.reviewerState.renderedMessage) + "\n")
			}
		}

		// Tool calls within this iteration block
		if len(it.toolCalls) > 0 {
			var heading string
			if it.llmStatus == "Executing tool calls..." {
				heading = "  🛠️  Executing Tool Calls:"
			} else {
				heading = "  🛠️  Tool Calls:"
			}
			toolHeadingStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("136")) // Ancient Bronze
			sb.WriteString(toolHeadingStyle.Render(heading) + "\n")

			toolNameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("216")) // Warm Amber / Peach
			toolArgsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // Muted Stone Grey

			statusQueued := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("queued")
			statusRunning := lipgloss.NewStyle().Foreground(lipgloss.Color("173")).Render("running...")
			statusDone := lipgloss.NewStyle().Foreground(lipgloss.Color("108")).Render("done")

			for _, tc := range it.toolCalls {
				var statusStr string
				argsStr := compactToolCallArgsString(tc.arguments)

				switch tc.status {
				case "queued":
					statusStr = fmt.Sprintf("    %s(%s): %s", toolNameStyle.Render(tc.name), toolArgsStyle.Render(argsStr), statusQueued)
				case "started":
					statusStr = fmt.Sprintf("    %s %s(%s): %s", m.spinner.View(), toolNameStyle.Render(tc.name), toolArgsStyle.Render(argsStr), statusRunning)
				case "completed":
					statusStr = fmt.Sprintf("    %s(%s): %s", toolNameStyle.Render(tc.name), toolArgsStyle.Render(argsStr), statusDone)
				case "failed":
					statusFailed := lipgloss.NewStyle().Foreground(lipgloss.Color("124")).Render(fmt.Sprintf("failed: %v", tc.err)) // Deep Crimson
					statusStr = fmt.Sprintf("    %s(%s): %s ⚠️", toolNameStyle.Render(tc.name), toolArgsStyle.Render(argsStr), statusFailed)
				}
				sb.WriteString(statusStr + "\n")
			}
			sb.WriteString("\n")
		}
	}

	// Section 3: Warnings
	if len(m.warnings) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render("⚠️  Warnings:") + "\n")
		for _, w := range m.warnings {
			sb.WriteString(fmt.Sprintf("  • %s\n", w))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func compactToolCallArgsString(args string) string {
	if args == "" {
		return "no args"
	}
	s := args
	const maxLen = 80
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

// tuiReporter implements Reporter to drive Bubble Tea UI.
type tuiReporter struct {
	mu      sync.Mutex
	program *tea.Program
	model   *tuiModel
	stdout  io.Writer
	stderr  io.Writer
	done    chan struct{}
	cancel  context.CancelFunc
	closed  bool
}

// NewTuiReporter constructs a TUI reporter.
func NewTuiReporter(stdout, stderr io.Writer, cancel context.CancelFunc) Reporter {
	m := &tuiModel{
		mcpServers: make(map[string]*mcpServerState),
	}
	m.spinner = spinner.New()
	m.spinner.Spinner = spinner.Dot
	m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))

	return &tuiReporter{
		model:  m,
		stdout: stdout,
		stderr: stderr,
		done:   make(chan struct{}),
		cancel: cancel,
	}
}

// startLocked starts the Bubble Tea program loop.
func (r *tuiReporter) startLocked() {
	p := tea.NewProgram(r.model, tea.WithOutput(r.stderr))
	r.program = p
	go func() {
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(r.stderr, "TUI program error: %v\n", err)
		}
		close(r.done)
		if r.model.userAborted && r.cancel != nil {
			r.cancel()
		}
	}()
}

// send sends a message to the Bubble Tea program.
func (r *tuiReporter) send(msg tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.program == nil {
		r.startLocked()
	}
	r.program.Send(msg)
}

func (r *tuiReporter) Close() error {
	r.mu.Lock()
	r.closed = true
	prog := r.program
	r.mu.Unlock()

	if prog != nil {
		prog.Send(quitMsg{})
		<-r.done

		// Print the final complete TUI progress content to stderr, bypassing the viewport
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("107")).Render("🛸 Cassandra AI Reviewer") + "\n\n"
		fmt.Fprint(r.stderr, title+r.model.renderContent()+"\n")

		r.mu.Lock()
		r.program = nil
		r.mu.Unlock()
	}
	return nil
}

func (r *tuiReporter) ReportPostReviewReply(message string) {
	fmt.Fprint(r.stderr, renderMarkdown(message, r.stderr)+"\n")
}

func (r *tuiReporter) ReportConfig(cfg *config.Config, targetDir string) {
	t := buildConfigTable(cfg, targetDir)

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("107")).
		Align(lipgloss.Left).
		Padding(0, 1)

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("179")).
		Padding(0, 1)

	valueStyle := lipgloss.NewStyle().
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

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

	r.model.configText = "\n" + t.Render() + "\n\n"

	// Trigger the lazy start of the TUI loop immediately after config display
	r.mu.Lock()
	if r.program == nil {
		r.startLocked()
	}
	r.mu.Unlock()
}

func (r *tuiReporter) ReportFetchingDiff() {
	// If the program hasn't started yet, print normally to stderr,
	// otherwise it's handled via start/event sequence.
	r.mu.Lock()
	started := r.program != nil
	r.mu.Unlock()

	if !started {
		fmt.Fprintln(r.stderr, "🌿 Fetching git diff...")
	}
}

func (r *tuiReporter) ReportFetchingCommits() {
	r.mu.Lock()
	started := r.program != nil
	r.mu.Unlock()

	if !started {
		fmt.Fprintln(r.stderr, "🌿 Fetching git commits...")
	}
}

func (r *tuiReporter) ReportNoChanges() {
	fmt.Fprintln(r.stderr, "⚪ No changes found.")
}

func (r *tuiReporter) ReportIteration(iter int) {
	r.send(iterationMsg{iter: iter})
}

func (r *tuiReporter) ReportToolCalls(tcs []llm.ToolCall) {
	r.send(toolCallsMsg{tcs: tcs})
	r.send(llmStatusMsg{status: "Executing tool calls...", llmWaiting: false})
}

func (r *tuiReporter) ReportUsage(usage llm.Usage) {
	// Not displayed in current TUI layout, but recorded
}

func (r *tuiReporter) ReportUsageSummary(total llm.Usage) {
	if total.PromptTokens > 0 || total.OutputTokens > 0 {
		fmt.Fprintf(r.stderr, "📈 %d in, %d out (total)\n", total.TotalInput(), total.TotalOutput())
	}
}

func (r *tuiReporter) ReportFinalReview() {
	r.send(llmStatusMsg{status: "Formulating final review...", llmWaiting: true})
}

func (r *tuiReporter) ReportExtraction() {
	r.send(llmStatusMsg{status: "Extracting findings...", llmWaiting: true})
}

func (r *tuiReporter) ReportExtractionRetry(attempt int) {
	r.send(llmStatusMsg{status: fmt.Sprintf("Extracting findings (attempt %d)...", attempt), llmWaiting: true})
}

func (r *tuiReporter) ReportEmptyResponseRetry(attempt int) {
	r.send(llmStatusMsg{status: fmt.Sprintf("Retrying empty response (attempt %d)...", attempt), llmWaiting: true})
}

func (r *tuiReporter) ReportCapReached(maxIterations int) {
	r.send(warningMsg{warning: fmt.Sprintf("Reached maximum ReAct iterations (%d). Forcing final review.", maxIterations)})
}

func (r *tuiReporter) ReportTruncated(maxTokens int) {
	r.send(warningMsg{warning: fmt.Sprintf("LLM response truncated (hit max-tokens limit of %d).", maxTokens)})
}

func (r *tuiReporter) ReportMCPStatus(name string, status string, err error) {
	r.send(mcpStatusMsg{name: name, status: status, err: err})
}

func (r *tuiReporter) ReportToolStatus(name string, status string, err error) {
	r.send(toolStatusMsg{name: name, status: status, err: err})
}

func (r *tuiReporter) ReportReviewHeader(files int, guidelines string, model string) {
	_ = r.Close()

	// Print once the TUI finishes to head the review output
	success := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("108")).Render("✅ Review generated successfully.")
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("107")).Render(fmt.Sprintf("# 📝 Review for %d files using %s (%s)", files, guidelines, model))
	fmt.Fprintf(r.stderr, "\n%s\n\n\n%s\n\n", success, title)
}

func (r *tuiReporter) ReportReview(result string) error {
	_ = r.Close()
	fmt.Fprint(r.stdout, renderMarkdown(result, r.stdout)+"\n")
	return nil
}

func (r *tuiReporter) ReportReviewWritten(file string) {
	fmt.Fprintf(r.stderr, "📝 Review written to %s\n", file)
}

func (r *tuiReporter) ReportStructuredReviewWritten(file string) {
	fmt.Fprintf(r.stderr, "📦 Structured review written to %s\n", file)
}

func (r *tuiReporter) ReportMetricsWritten(file string) {
	fmt.Fprintf(r.stderr, "📈 Metrics written to %s\n", file)
}

func (r *tuiReporter) ReportWarning(msg string, err error) {
	r.mu.Lock()
	started := r.program != nil
	r.mu.Unlock()

	var warnStr string
	if err != nil {
		warnStr = fmt.Sprintf("%s: %v", msg, err)
	} else {
		warnStr = msg
	}

	if started {
		r.send(warningMsg{warning: warnStr})
	} else {
		fmt.Fprintf(r.stderr, "⚠️  %s\n", warnStr)
	}
}

func (r *tuiReporter) ReportError(err error) {
	fmt.Fprintf(r.stderr, "Error: %v\n", err)
}

func (r *tuiReporter) NotifyUser() {
	fmt.Fprint(r.stderr, "\a")
}
