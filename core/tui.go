package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
	"golang.org/x/term"
)

// TUI message types
type mcpStatusMsg struct {
	name   string
	status string
	err    error
}

type toolStatusMsg struct {
	id     string
	name   string
	status string
	err    error
}

type toolCallsMsg struct {
	tcs   []llm.ToolCall
	stage string
	iter  int
}

type iterationMsg struct {
	iter  int
	stage string
}

type llmStatusMsg struct {
	stage      string
	iter       int
	status     string
	llmWaiting bool
}

type warningMsg struct {
	stage   string
	warning string
}

// usageSummaryMsg carries a cumulative snapshot, not an incremental token count.
type usageSummaryMsg struct {
	stage string
	total llm.Usage
}

type quitMsg struct{}

type stageReviewMsg struct {
	title  string
	result string
}

type stageReviewState struct {
	stageReviewMsg
	afterIterations int
}

// TUI state representation
type mcpServerState struct {
	name   string
	status string
	err    error
}

type toolCallState struct {
	id        string
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
	stage         string
	llmStatus     string
	llmWaiting    bool
	reviewerState *reviewerState
	toolCalls     []*toolCallState
}

type tuiModel struct {
	mcpServers     map[string]*mcpServerState
	mcpList        []string
	iterations     []*iterationState
	warnings       []string
	quitting       bool
	spinner        spinner.Model
	viewport       viewport.Model
	ready          bool
	configText     string
	userAborted    bool
	stageReviews   []stageReviewState
	usageSummaries []usageSummaryMsg
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
		if current := m.findIteration(msg.stage, msg.iter); current != nil {
			current.llmWaiting = false

			var standardCalls []*toolCallState
			var revState *reviewerState

			for _, tc := range msg.tcs {
				if focusArea, message, ok := parseReviewerState(tc); ok {
					revState = &reviewerState{
						focusArea:       focusArea,
						message:         message,
						renderedMessage: renderMarkdown(message, os.Stderr),
					}
				} else {
					standardCalls = append(standardCalls, &toolCallState{
						id:        tc.ID,
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
			matchesCall := func(tc *toolCallState) bool {
				if msg.id != "" {
					return tc.id == msg.id
				}
				return tc.name == msg.name
			}
			// Namespaced IDs identify calls even after another stage starts an iteration.
			// Without an ID, preserve the legacy current-iteration name fallback.
		iterations:
			for i := len(m.iterations) - 1; i >= 0; i-- {
				for _, tc := range m.iterations[i].toolCalls {
					if !matchesCall(tc) {
						continue
					}
					if msg.status == "started" && tc.status == "queued" {
						tc.status = msg.status
						break iterations
					}
					if (msg.status == "completed" || msg.status == "failed") && (tc.status == "started" || tc.status == "queued") {
						tc.status = msg.status
						tc.err = msg.err
						break iterations
					}
				}
				if msg.id == "" {
					break
				}
			}
		}
		autoScroll = true

	case iterationMsg:
		m.iterations = append(m.iterations, &iterationState{
			iter:       msg.iter,
			stage:      msg.stage,
			llmStatus:  "Waiting for LLM reply...",
			llmWaiting: true,
		})
		autoScroll = true

	case llmStatusMsg:
		if current := m.findIteration(msg.stage, msg.iter); current != nil {
			current.llmStatus = msg.status
			current.llmWaiting = msg.llmWaiting
		}
		autoScroll = true

	case warningMsg:
		warning := msg.warning
		if msg.stage != "" {
			warning = fmt.Sprintf("[%s] %s", msg.stage, warning)
		}
		m.warnings = append(m.warnings, warning)
		autoScroll = true

	case usageSummaryMsg:
		m.finishStage(msg.stage)
		if !hasReportableUsage(msg.total) {
			break
		}
		found := false
		for i := range m.usageSummaries {
			if m.usageSummaries[i].stage == msg.stage {
				m.usageSummaries[i] = msg
				found = true
				break
			}
		}
		if !found {
			m.usageSummaries = append(m.usageSummaries, msg)
		}
		autoScroll = true

	case stageReviewMsg:
		if msg.title != "" {
			m.finishStage("Subagent: " + msg.title)
		}
		m.stageReviews = append(m.stageReviews, stageReviewState{
			stageReviewMsg:  msg,
			afterIterations: len(m.iterations),
		})
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

func (m *tuiModel) finishStage(stage string) {
	if stage == "" {
		return
	}
	if current := m.findIteration(stage, 0); current != nil && current.llmWaiting {
		current.llmWaiting = false
		if strings.HasPrefix(current.llmStatus, "Formulating final ") {
			current.llmStatus = "Stage completed."
		}
	}
}

// findIteration resolves stage-scoped callbacks without moving other stages' state.
// Empty stage matches any non-subagent iteration (unstaged main-agent events
// must not land on a nested dynamic subagent); zero iter selects the most
// recent iteration for a stage. A nonzero iter must always match.
func (m *tuiModel) findIteration(stage string, iter int) *iterationState {
	for i := len(m.iterations) - 1; i >= 0; i-- {
		current := m.iterations[i]
		stageMatch := current.stage == stage || (stage == "" && !strings.HasPrefix(current.stage, "Subagent: "))
		if stageMatch && (iter == 0 || current.iter == iter) {
			return current
		}
	}
	return nil
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
	for i, it := range m.iterations {
		m.renderStageReviews(&sb, i)

		label := fmt.Sprintf("🔍 [Iteration %d]", it.iter)
		if strings.HasPrefix(it.stage, "Subagent: ") {
			label = fmt.Sprintf("%s [%s - Iteration %d]", subagentIcon, it.stage, it.iter)
		} else if it.stage != "" {
			label = fmt.Sprintf("🔍 [%s - Iteration %d]", it.stage, it.iter)
		}
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("178")).Render(label) + "\n")

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
				name := tc.name
				if title, _, ok := parseRunSubagent(llm.ToolCall{Name: tc.name, Arguments: tc.arguments}); ok {
					name, argsStr = subagentIcon+" Subagent", title
				}

				switch tc.status {
				case "queued":
					statusStr = fmt.Sprintf("    %s(%s): %s", toolNameStyle.Render(name), toolArgsStyle.Render(argsStr), statusQueued)
				case "started":
					statusStr = fmt.Sprintf("    %s %s(%s): %s", m.spinner.View(), toolNameStyle.Render(name), toolArgsStyle.Render(argsStr), statusRunning)
				case "completed":
					statusStr = fmt.Sprintf("    %s(%s): %s", toolNameStyle.Render(name), toolArgsStyle.Render(argsStr), statusDone)
				case "failed":
					statusFailed := lipgloss.NewStyle().Foreground(lipgloss.Color("124")).Render(fmt.Sprintf("failed: %v", tc.err)) // Deep Crimson
					statusStr = fmt.Sprintf("    %s(%s): %s ⚠️", toolNameStyle.Render(name), toolArgsStyle.Render(argsStr), statusFailed)
				}
				sb.WriteString(statusStr + "\n")
			}
			sb.WriteString("\n")
		}
	}

	m.renderStageReviews(&sb, len(m.iterations))

	// Section 3: Warnings
	if len(m.warnings) > 0 {
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Render("⚠️  Warnings:") + "\n")
		for _, w := range m.warnings {
			sb.WriteString(fmt.Sprintf("  • %s\n", w))
		}
		sb.WriteString("\n")
	}

	for _, summary := range m.usageSummaries {
		label := ""
		if summary.stage != "" {
			label = fmt.Sprintf("[%s] ", summary.stage)
		}
		fmt.Fprintf(&sb, "📈 %s%d in, %d out (total)\n", label, summary.total.TotalInput(), summary.total.TotalOutput())
	}

	return sb.String()
}

func (m *tuiModel) renderStageReviews(sb *strings.Builder, afterIterations int) {
	for _, review := range m.stageReviews {
		if review.afterIterations != afterIterations {
			continue
		}
		title := fmt.Sprintf("📋 %s:", review.title)
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("108")).Render(title) + "\n")
		sb.WriteString(indentText(renderMarkdown(review.result, os.Stderr), 2) + "\n\n")
	}
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
	mu        sync.Mutex
	promptMu  sync.Mutex
	program   *tea.Program
	model     *tuiModel
	stdout    io.Writer
	stderr    io.Writer
	done      chan struct{}
	cancel    context.CancelFunc
	closed    bool
	closeOnce sync.Once
}

var (
	_ Reporter                = (*tuiReporter)(nil)
	_ tools.DeveloperPrompter = (*tuiReporter)(nil)
)

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
	p := tea.NewProgram(r.model, tea.WithOutput(r.stderr), tea.WithInput(strings.NewReader("")))
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
	r.closeOnce.Do(func() {
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
	})
	return nil
}

func (r *tuiReporter) ReportPostReviewReply(message string) {
	fmt.Fprint(r.stderr, renderMarkdown(message, r.stderr)+"\n")
}

func (r *tuiReporter) ReportPostReviewUserQuery(query string) {
	width := 80
	if f, ok := r.stderr.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			width = w
		}
	}
	wrapWidth := width - 8
	if wrapWidth < 40 {
		wrapWidth = 40
	}
	rendered := renderMarkdownWithWidth(query, wrapWidth)

	// Trim trailing spaces from each line to prevent box stretching
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	content := strings.TrimSpace(strings.Join(lines, "\n"))

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("108")).Render("💬 User:")
	boxContent := title + "\n" + content
	styledBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("108")).
		Padding(0, 1).
		Render(boxContent)
	fmt.Fprint(r.stderr, "\n"+styledBox+"\n")
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

func (r *tuiReporter) ReportStageIteration(stage string, iter int) {
	r.send(iterationMsg{iter: iter, stage: stage})
}

func (r *tuiReporter) ReportToolCalls(tcs []llm.ToolCall) {
	r.send(toolCallsMsg{tcs: tcs})
}

// ReportStageToolCalls associates tool batches with the originating pipeline
// stage when concurrent subagents interleave their reporting callbacks.
func (r *tuiReporter) ReportStageToolCalls(stage string, iter int, calls []llm.ToolCall) {
	r.send(toolCallsMsg{stage: stage, iter: iter, tcs: calls})
}

func (r *tuiReporter) ReportUsage(usage llm.Usage) {
	// Not displayed in current TUI layout, but recorded
}

func (r *tuiReporter) ReportUsageSummary(total llm.Usage) {
	r.ReportStageUsageSummary("", total)
}

// ReportStageUsageSummary replaces the displayed cumulative total for a stage.
func (r *tuiReporter) ReportStageUsageSummary(stage string, total llm.Usage) {
	r.send(usageSummaryMsg{stage: stage, total: total})
}

func (r *tuiReporter) ReportFinalReview() {
	r.send(llmStatusMsg{status: "Formulating final review...", llmWaiting: true})
}

func (r *tuiReporter) ReportStageFinalReview(stage string) {
	r.send(llmStatusMsg{stage: stage, status: fmt.Sprintf("Formulating final %s...", strings.ToLower(stage)), llmWaiting: true})
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

// ReportStageEmptyResponseRetry targets the iteration that produced the empty reply.
func (r *tuiReporter) ReportStageEmptyResponseRetry(stage string, iter int, attempt int) {
	r.send(llmStatusMsg{stage: stage, iter: iter, status: fmt.Sprintf("Retrying empty response (attempt %d)...", attempt), llmWaiting: true})
}

func (r *tuiReporter) ReportCapReached(maxIterations int) {
	r.ReportStageCapReached("", maxIterations)
}

// ReportStageCapReached attributes the iteration-limit warning to its stage.
func (r *tuiReporter) ReportStageCapReached(stage string, cap int) {
	r.ReportStageWarning(stage, fmt.Sprintf("Reached maximum ReAct iterations (%d). Forcing final review.", cap), nil)
}

func (r *tuiReporter) ReportTruncated(maxTokens int) {
	r.ReportStageTruncated("", maxTokens)
}

// ReportStageTruncated attributes the token-limit warning to its stage.
func (r *tuiReporter) ReportStageTruncated(stage string, tokens int) {
	r.ReportStageWarning(stage, fmt.Sprintf("LLM response truncated (hit max-tokens limit of %d).", tokens), nil)
}

// ReportStageWarning keeps stage diagnostics within the Bubble Tea event loop.
func (r *tuiReporter) ReportStageWarning(stage, msg string, err error) {
	if err != nil {
		msg = fmt.Sprintf("%s: %v", msg, err)
	}
	r.send(warningMsg{stage: stage, warning: msg})
}

func (r *tuiReporter) ReportMCPStatus(name string, status string, err error) {
	r.send(mcpStatusMsg{name: name, status: status, err: err})
}

func (r *tuiReporter) ReportToolStatus(tc llm.ToolCall, status string, err error) {
	r.send(toolStatusMsg{id: tc.ID, name: tc.Name, status: status, err: err})
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

func (r *tuiReporter) ReportStageReview(stage, result string) error {
	r.send(stageReviewMsg{title: stage, result: result})
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
	r.promptMu.Lock()
	defer r.promptMu.Unlock()

	r.mu.Lock()
	prog := r.program
	r.mu.Unlock()

	if prog != nil {
		_ = prog.ReleaseTerminal()
		defer func() {
			_ = prog.RestoreTerminal()
		}()
	}
	fmt.Fprint(r.stderr, "\a")
}

func indentText(text string, indent int) string {
	prefix := strings.Repeat(" ", indent)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// AskDeveloper implements tools.DeveloperPrompter. It releases the terminal
// so that the interactive huh form can prompt the developer without interference
// from the background Bubble Tea loop, then restores the terminal afterwards.
func (r *tuiReporter) AskDeveloper(ctx context.Context, req tools.AskDeveloperRequest) (tools.AskDeveloperResponse, error) {
	r.promptMu.Lock()
	defer r.promptMu.Unlock()

	r.mu.Lock()
	prog := r.program
	r.mu.Unlock()

	if prog != nil {
		_ = prog.ReleaseTerminal()
		defer func() {
			_ = prog.RestoreTerminal()
		}()
	}

	return tools.DefaultTerminalPrompter{}.AskDeveloper(ctx, req)
}
