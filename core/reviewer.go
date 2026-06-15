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

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/factory"
	"github.com/menny/cassandra/tools"
	"github.com/menny/cassandra/tools/mcp"
	"golang.org/x/term"
)

// Reviewer encapsulates an initialized Agent and its environment.
type Reviewer struct {
	Agent                    *Agent
	Config                   *config.Config
	RootDir                  string
	StablePrompt             string
	Guidelines               string
	SupplementalGuidelines   []string
	ApprovalEvaluationPrompt string
	mcpManager               *mcp.Manager
}

// NewReviewer instantiates a Reviewer based on the provided configuration.
// targetDir is the directory where local tools (like grep) will operate.
func NewReviewer(ctx context.Context, cfg *config.Config, targetDir string, reporter Reporter) (r *Reviewer, err error) {
	if reporter == nil {
		reporter = NewRawReporter(os.Stdout, os.Stderr)
	}

	client, err := factory.New(ctx, cfg.Provider, cfg.Model, cfg.ProviderAPIKey, cfg.ProviderURL, cfg.ProviderOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize LLM: %w", err)
	}

	registry := tools.NewRegistry()
	var notifier tools.UserNotifier = reporter
	if cfg.Render != "markdown" && cfg.Render != "tui" {
		notifier = tools.NoOpUserNotifier{}
	}
	tools.RegisterLocalTools(registry, targetDir, cfg.IgnoredLockFiles, cfg.WishlistDir, cfg.AllowAskDeveloper, notifier)

	var mcpManager *mcp.Manager
	// Ensure we close the MCP manager if we encounter an error later in this function.
	defer func() {
		if err != nil && mcpManager != nil {
			_ = mcpManager.Close()
		}
	}()

	var mcpConfig mcp.Config
	if cfg.MCPConfigFile != "" {
		mcpData, err := os.ReadFile(cfg.MCPConfigFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read MCP config file %s: %w", cfg.MCPConfigFile, err)
		}
		if err := json.Unmarshal(mcpData, &mcpConfig); err != nil {
			return nil, fmt.Errorf("failed to parse MCP config file %s: %w", cfg.MCPConfigFile, err)
		}
	}

	if cfg.AllowURLFetch {
		if mcpConfig.MCPServers == nil {
			mcpConfig.MCPServers = make(map[string]mcp.ServerConfig)
		}
		mcpConfig.MCPServers["mcp-server-fetch"] = mcp.ServerConfig{
			Command: "uvx",
			Args:    []string{"mcp-server-fetch"},
		}
	}

	if len(mcpConfig.MCPServers) > 0 {
		mcpConfig.ExpandEnv()
		mcpManager = mcp.NewManager()
		var mu sync.Mutex
		if err := mcpManager.RegisterServers(
			ctx,
			mcpConfig,
			reporter.ReportMCPStatus,
			reporter.ReportWarning,
			func(def llm.ToolDef, handler func(context.Context, llm.ToolCall) (string, error)) {
				mu.Lock()
				registry.RegisterTool(def, handler)
				mu.Unlock()
			},
		); err != nil {
			return nil, fmt.Errorf("failed to register MCP servers: %w", err)
		}
	}

	mainGuidelines, err := config.ResolveGuidelinesContent(cfg.MainGuidelines)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve main guidelines: %w", err)
	}

	var supplementalGuidelines []string
	for _, sg := range cfg.SupplementalGuidelines {
		content, err := config.ResolveGuidelinesContent(sg)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve supplemental guideline %q: %w", sg, err)
		}
		supplementalGuidelines = append(supplementalGuidelines, content)
	}

	var approvalEvaluationPrompt string
	if cfg.ApprovalEvaluationPromptFile != "" {
		content, err := os.ReadFile(cfg.ApprovalEvaluationPromptFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read approval evaluation prompt file: %w", err)
		}
		approvalEvaluationPrompt = string(content)
	}

	stable, _, _, err := prompts.BuildSystemPrompt(targetDir, nil, mainGuidelines, supplementalGuidelines, approvalEvaluationPrompt, cfg.AllowAskDeveloper)
	if err != nil {
		return nil, fmt.Errorf("failed to build stable system prompt: %w", err)
	}

	return &Reviewer{
		Agent:                    NewAgent(client, registry, WithReporter(reporter)),
		Config:                   cfg,
		RootDir:                  targetDir,
		StablePrompt:             stable,
		Guidelines:               mainGuidelines,
		SupplementalGuidelines:   supplementalGuidelines,
		ApprovalEvaluationPrompt: approvalEvaluationPrompt,
		mcpManager:               mcpManager,
	}, nil
}

// Close releases resources (like MCP server connections).
func (r *Reviewer) Close() error {
	if r.mcpManager != nil {
		return r.mcpManager.Close()
	}
	return nil
}

// Run executes a review for the given changes.
func (r *Reviewer) Run(ctx context.Context, changedFiles []string, requestText string) (string, error) {
	_, dynamic, _, err := prompts.BuildSystemPrompt(r.RootDir, changedFiles, r.Guidelines, r.SupplementalGuidelines, r.ApprovalEvaluationPrompt, r.Config.AllowAskDeveloper)
	if err != nil {
		return "", fmt.Errorf("failed to build dynamic system prompt: %w", err)
	}

	maxIterations := CalculateMaxIterations(len(changedFiles))
	return r.Agent.RunReview(ctx, r.StablePrompt, dynamic, requestText, maxIterations, r.Config.MaxTokens)
}

const postReviewSystemInstruction = "You have completed the automated code review. " +
	"You are now in an interactive post-review chat phase. " +
	"Transition purely into a conversational assistant. Answer the developer's questions, " +
	"defend your findings, or admit mistakes based on the user's input. " +
	"Do NOT attempt to rewrite the code review or output a new structured code review payload."

type (
	replStdinKey  struct{}
	replStderrKey struct{}
)

// WithTestREPLStreams wraps a context to override standard input/stderr of the interactive REPL.
func WithTestREPLStreams(ctx context.Context, in io.Reader, err io.Writer) context.Context {
	ctx = context.WithValue(ctx, replStdinKey{}, in)
	return context.WithValue(ctx, replStderrKey{}, err)
}

type terminalSpinner struct {
	mu       sync.Mutex
	active   bool
	frames   []string
	delay    time.Duration
	stopChan chan struct{}
	doneChan chan struct{}
	writer   io.Writer
}

func newTerminalSpinner(w io.Writer) *terminalSpinner {
	return &terminalSpinner{
		frames:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		delay:    80 * time.Millisecond,
		writer:   w,
		stopChan: make(chan struct{}),
	}
}

func (s *terminalSpinner) Start(message string) {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	s.active = true
	s.stopChan = make(chan struct{})
	s.doneChan = make(chan struct{})
	stopChan := s.stopChan
	doneChan := s.doneChan
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(s.delay)
		defer ticker.Stop()
		defer func() {
			fmt.Fprintf(s.writer, "\r\033[K")
			close(doneChan)
		}()
		i := 0
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				s.mu.Lock()
				if !s.active {
					s.mu.Unlock()
					return
				}
				frame := s.frames[i%len(s.frames)]
				styledFrame := lipgloss.NewStyle().Foreground(lipgloss.Color("178")).Render(frame)
				fmt.Fprintf(s.writer, "\r%s %s", styledFrame, message)
				s.mu.Unlock()
				i++
			}
		}
	}()
}

func (s *terminalSpinner) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	close(s.stopChan)
	doneChan := s.doneChan
	s.mu.Unlock()

	<-doneChan
}

// RunInteractivePostReview starts a continuous chat loop with the developer.
func (r *Reviewer) RunInteractivePostReview(ctx context.Context) error {
	replCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var input io.Reader = os.Stdin
	if in, ok := ctx.Value(replStdinKey{}).(io.Reader); ok {
		input = in
	}
	var errWriter io.Writer = os.Stderr
	if err, ok := ctx.Value(replStderrKey{}).(io.Writer); ok {
		errWriter = err
	}

	// Defensive goroutine to unblock any blocking read in accessible mode / tests
	// by closing the input stream if it implements io.Closer and is not os.Stdin.
	if replCtx.Done() != nil {
		go func() {
			<-replCtx.Done()
			if closer, ok := input.(io.Closer); ok && input != os.Stdin {
				_ = closer.Close()
			}
		}()
	}

	// Append system instruction indicating conversational post-review phase
	r.Agent.history = append(r.Agent.history, llm.Message{
		Role: llm.RoleSystem,
		Text: postReviewSystemInstruction,
	})

	maxIterations := CalculateMaxIterations(1)

	accessible := !term.IsTerminal(int(os.Stdin.Fd()))
	if ctx.Value(replStdinKey{}) != nil {
		accessible = true
	}

	for {
		if replCtx.Err() != nil {
			return nil
		}

		width := 80
		if f, ok := errWriter.(*os.File); ok {
			if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
				width = w
			}
		}

		if r.Config.Render == "tui" || r.Config.Render == "markdown" {
			divider := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("\n" + strings.Repeat("─", width) + "\n")
			fmt.Fprint(errWriter, divider)
		}

		var userInput string
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewText().
					Title("Ask Cassandra").
					Value(&userInput).
					Lines(8).
					WithWidth(width),
			),
		).WithWidth(width)

		theme := huh.ThemeCharm()
		theme.Focused.Title = theme.Focused.Title.Foreground(lipgloss.Color("216")).Bold(true)
		theme.Focused.Description = theme.Focused.Description.Foreground(lipgloss.Color("243"))
		theme.Focused.Base = theme.Focused.Base.BorderLeft(true).BorderForeground(lipgloss.Color("107"))
		theme.Focused.TextInput.Prompt = theme.Focused.TextInput.Prompt.Foreground(lipgloss.Color("178"))
		theme.Focused.TextInput.Text = theme.Focused.TextInput.Text.Foreground(lipgloss.Color("223"))

		form.WithTheme(theme).
			WithInput(input).
			WithOutput(errWriter).
			WithAccessible(accessible)

		err := form.RunWithContext(replCtx)
		if err != nil {
			if replCtx.Err() != nil || errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("interactive prompt failed: %w", err)
		}

		cleanInput := strings.TrimSpace(userInput)
		lowerInput := strings.ToLower(cleanInput)
		if lowerInput == "exit" || lowerInput == "bye" || lowerInput == "/exit" {
			return nil
		}

		if cleanInput == "" {
			continue
		}

		var spinner *terminalSpinner
		if r.Config.Render == "tui" {
			spinner = newTerminalSpinner(errWriter)
			spinner.Start("Cassandra is thinking...")
		}

		reply, err := r.Agent.RunChatFlight(replCtx, cleanInput, maxIterations, r.Config.MaxTokens)
		if spinner != nil {
			spinner.Stop()
		}
		if err != nil {
			return fmt.Errorf("chat flight failed: %w", err)
		}

		r.Agent.reporter.ReportPostReviewReply(reply)
	}
}
