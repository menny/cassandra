package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
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
	focusArea string
	message   string
}

type iterationState struct {
	iter          int
	llmStatus     string
	llmWaiting    bool
	reviewerState *reviewerState
	toolCalls     []*toolCallState
}

type tuiModel struct {
	mcpServers map[string]*mcpServerState
	mcpList    []string
	iterations []*iterationState
	warnings   []string
	quitting   bool
	spinner    spinner.Model
}

func (m tuiModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case mcpStatusMsg:
		state, exists := m.mcpServers[msg.name]
		if !exists {
			state = &mcpServerState{name: msg.name}
			m.mcpServers[msg.name] = state
			m.mcpList = append(m.mcpList, msg.name)
		}
		state.status = msg.status
		state.err = msg.err
		return m, nil

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
						focusArea: args.FocusArea,
						message:   args.Message,
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
		return m, nil

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
		return m, nil

	case iterationMsg:
		m.iterations = append(m.iterations, &iterationState{
			iter:       msg.iter,
			llmStatus:  "Waiting for LLM reply...",
			llmWaiting: true,
		})
		return m, nil

	case llmStatusMsg:
		if len(m.iterations) > 0 {
			current := m.iterations[len(m.iterations)-1]
			current.llmStatus = msg.status
			current.llmWaiting = msg.llmWaiting
		}
		return m, nil

	case warningMsg:
		m.warnings = append(m.warnings, msg.warning)
		return m, nil

	case quitMsg:
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}

	var sb strings.Builder

	// Header
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86")).Render("🛸 Cassandra AI Reviewer") + "\n\n")

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
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99")).Render(fmt.Sprintf("🔍 [Iteration %d]", it.iter)) + "\n")

		if it.llmWaiting {
			sb.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), it.llmStatus))
		} else if it.reviewerState == nil && len(it.toolCalls) == 0 {
			sb.WriteString(fmt.Sprintf("  %s\n", it.llmStatus))
		}

		// Focus area / Reviewer state messages
		if it.reviewerState != nil {
			if len(it.reviewerState.focusArea) > 0 {
				sb.WriteString(fmt.Sprintf("  🧠 [Reviewer state] focus area: %s\n", it.reviewerState.focusArea))
			} else {
				sb.WriteString("  🧠 [Reviewer state]\n")
			}
			if len(it.reviewerState.message) > 0 {
				msgStyle := lipgloss.NewStyle().MarginLeft(4).MarginBottom(1)
				sb.WriteString(msgStyle.Render(it.reviewerState.message) + "\n")
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
			sb.WriteString(heading + "\n")

			for _, tc := range it.toolCalls {
				var statusStr string
				argsStr := compactToolCallArgsString(tc.arguments)

				switch tc.status {
				case "queued":
					statusStr = fmt.Sprintf("    %s(%s): queued", tc.name, argsStr)
				case "started":
					statusStr = fmt.Sprintf("    %s %s(%s): running...", m.spinner.View(), tc.name, argsStr)
				case "completed":
					statusStr = fmt.Sprintf("    %s(%s): done", tc.name, argsStr)
				case "failed":
					statusStr = fmt.Sprintf("    %s(%s): failed: %v ⚠️", tc.name, argsStr, tc.err)
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
}

// NewTuiReporter constructs a TUI reporter.
func NewTuiReporter(stdout, stderr io.Writer, cancel context.CancelFunc) Reporter {
	m := &tuiModel{
		mcpServers: make(map[string]*mcpServerState),
	}
	m.spinner = spinner.New()
	m.spinner.Spinner = spinner.Dot
	m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

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
		if r.cancel != nil {
			r.cancel()
		}
	}()
}

// send sends a message to the Bubble Tea program.
func (r *tuiReporter) send(msg tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.program == nil {
		r.startLocked()
	}
	r.program.Send(msg)
}

func (r *tuiReporter) Close() error {
	r.mu.Lock()
	prog := r.program
	r.mu.Unlock()

	if prog != nil {
		prog.Send(quitMsg{})
		<-r.done
	}
	return nil
}

func (r *tuiReporter) ReportConfig(cfg *config.Config, targetDir string) {
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

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		Align(lipgloss.Left).
		Padding(0, 1)

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("81")).
		Padding(0, 1)

	valueStyle := lipgloss.NewStyle().
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

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

	fmt.Fprint(r.stderr, "\n"+t.Render()+"\n\n")

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
	// Print once the TUI finishes to head the review output
	fmt.Fprintf(r.stderr, "\n✅ Review generated successfully.\n\n\n# 📝 Review for %d files using %s (%s)\n\n", files, guidelines, model)
}

func (r *tuiReporter) ReportReview(result string) error {
	_ = r.Close()
	fmt.Fprint(r.stdout, result+"\n")
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
