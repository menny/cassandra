package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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
	"github.com/menny/cassandra/util"
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
	llmFactory               func(ctx context.Context, provider, modelName, apiKey, baseURL string, options map[string]any) (llm.Model, error)
	PreReviewMetrics         *SessionMetrics
	CodeReviewMetrics        *SessionMetrics
	DiscussionMetrics        *SessionMetrics
	diffMap                  map[string]string

	verdictMu      sync.RWMutex
	currentVerdict ReviewVerdict
}

// SetVerdict sets the current review verdict in a thread-safe manner.
func (r *Reviewer) SetVerdict(v ReviewVerdict) {
	r.verdictMu.Lock()
	defer r.verdictMu.Unlock()
	r.currentVerdict = v
}

// GetVerdict returns the current review verdict in a thread-safe manner.
func (r *Reviewer) GetVerdict() ReviewVerdict {
	r.verdictMu.RLock()
	defer r.verdictMu.RUnlock()
	return r.currentVerdict
}

func allowAskDeveloper(cfg *config.Config) bool {
	return cfg != nil && cfg.AllowAskDeveloper
}

func isACPSession(cfg *config.Config) bool {
	return cfg != nil && cfg.Render == "acp"
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
	if p, ok := reporter.(tools.DeveloperPrompter); ok {
		registry.SetDeveloperPrompter(p)
	}
	var notifier tools.UserNotifier = reporter
	if cfg.Render != "markdown" && cfg.Render != "tui" {
		notifier = tools.NoOpUserNotifier{}
	}
	var rReviewer *Reviewer
	includeAskDeveloper := allowAskDeveloper(cfg)
	includePRComment := isACPSession(cfg)
	tools.RegisterLocalTools(
		registry,
		targetDir,
		cfg.Base,
		cfg.Head,
		cfg.IgnoredLockFiles,
		cfg.WishlistDir,
		includeAskDeveloper,
		includePRComment,
		notifier,
		func() map[string]string {
			if rReviewer != nil {
				return rReviewer.diffMap
			}
			return nil
		},
		cfg.MetadataJSONFile,
	)

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
			Args:    []string{"--with", "mcp<1.3.0", "mcp-server-fetch"},
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
			reporter.ReportWarning("Failed to register MCP servers; proceeding without them", err)
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

	var currentModel string
	if cfg != nil {
		currentModel = cfg.Model
	}
	stable, _, _, err := prompts.BuildSystemPrompt(targetDir, nil, mainGuidelines, supplementalGuidelines, approvalEvaluationPrompt, includeAskDeveloper, tools.IsT3Session(), includePRComment, currentModel)
	if err != nil {
		return nil, fmt.Errorf("failed to build stable system prompt: %w", err)
	}

	rReviewer = &Reviewer{
		Agent:                    NewAgent(client, registry, WithReporter(reporter)),
		Config:                   cfg,
		RootDir:                  targetDir,
		StablePrompt:             stable,
		Guidelines:               mainGuidelines,
		SupplementalGuidelines:   supplementalGuidelines,
		ApprovalEvaluationPrompt: approvalEvaluationPrompt,
		mcpManager:               mcpManager,
		llmFactory:               factory.New,
	}
	registry.SetVerdictChangeListener(func(ctx context.Context, verdict string, rationale string) error {
		rReviewer.SetVerdict(ReviewVerdict(verdict))
		return nil
	})
	return rReviewer, nil
}

// SetDiffMap sets the pre-parsed in-memory file diff map for on-demand diff retrieval.
func (r *Reviewer) SetDiffMap(diffMap map[string]string) {
	r.diffMap = diffMap
}

// SetDiff parses the unified diff and sets the in-memory file diff map.
func (r *Reviewer) SetDiff(diffText string) {
	r.diffMap = util.SplitUnifiedDiff(diffText)
}

// Close releases resources (like MCP server connections).
func (r *Reviewer) Close() error {
	if r.mcpManager != nil {
		return r.mcpManager.Close()
	}
	return nil
}

// SetSession sets or updates the SessionStore and session ID on the Reviewer's Agent.
func (r *Reviewer) SetSession(store SessionStore, sessionID string) {
	if r.Agent != nil {
		r.Agent.SetSession(store, sessionID)
	}
}

// SetHistory sets the conversation history on the Reviewer's Agent.
func (r *Reviewer) SetHistory(history []llm.Message) {
	if r.Agent != nil {
		r.Agent.SetHistory(history)
	}
}

func (r *Reviewer) waitAndAppendInitialUserContext(ctx context.Context, requestText string) (string, error) {
	server, ok := r.Agent.reporter.(PostReviewServer)
	if !ok {
		return "", errors.New("ACP mode requires a reporter that implements PostReviewServer")
	}
	firstMsg, err := server.WaitForFirstMessage(ctx)
	if err != nil {
		return "", err
	}
	if trimmed := strings.TrimSpace(firstMsg); trimmed != "" {
		requestText += "\n\n<extra_initial_user_context_or_guidelines>\n" + trimmed + "\n</extra_initial_user_context_or_guidelines>"
	}
	return requestText, nil
}

// Run executes a review for the given changes.
func (r *Reviewer) Run(ctx context.Context, changedFiles []string, requestText string) (string, error) {
	if r.Config != nil && r.Config.WaitForMessage {
		var err error
		requestText, err = r.waitAndAppendInitialUserContext(ctx, requestText)
		if err != nil {
			return "", err
		}
	}

	var currentModel string
	if r.Config != nil {
		currentModel = r.Config.Model
	}
	_, dynamic, _, err := prompts.BuildSystemPrompt(r.RootDir, changedFiles, r.Guidelines, r.SupplementalGuidelines, r.ApprovalEvaluationPrompt, allowAskDeveloper(r.Config), tools.IsT3Session(), isACPSession(r.Config), currentModel)
	if err != nil {
		return "", fmt.Errorf("failed to build dynamic system prompt: %w", err)
	}

	if r.Config.PreReviewPromptFile != "" {
		preReviewPromptBytes, err := os.ReadFile(r.Config.PreReviewPromptFile)
		if err != nil {
			return "", fmt.Errorf("failed to read pre-review prompt file %q: %w", r.Config.PreReviewPromptFile, err)
		}
		preReviewPrompt := string(preReviewPromptBytes)

		originalLLM := r.Agent.llm
		if r.Config.PreReviewModel != "" && r.Config.PreReviewModel != r.Config.Model {
			newFactory := r.llmFactory
			if newFactory == nil {
				newFactory = factory.New
			}
			preLLM, err := newFactory(ctx, r.Config.Provider, r.Config.PreReviewModel, r.Config.ProviderAPIKey, r.Config.ProviderURL, r.Config.ProviderOptions)
			if err != nil {
				return "", fmt.Errorf("failed to initialize pre-review LLM: %w", err)
			}
			r.Agent.llm = preLLM
		}
		defer func() {
			r.Agent.llm = originalLLM
		}()

		r.Agent.Stage = "Pre-Review Summary"
		r.Agent.ResetMetrics()
		maxIterations := CalculateMaxIterations(len(changedFiles))
		summary, err := r.Agent.RunReview(ctx, preReviewPrompt, dynamic, requestText, maxIterations, r.Config.MaxTokens)
		preMetrics := r.Agent.GetMetrics()
		r.PreReviewMetrics = &preMetrics
		if err != nil {
			return "", fmt.Errorf("pre-review summary phase failed: %w", err)
		}

		if err := r.Agent.reporter.ReportStageReview("Pre-Review Summary", summary); err != nil {
			r.Agent.reporter.ReportWarning("Failed to display pre-review summary", err)
		}

		r.Agent.llm = originalLLM

		var sb strings.Builder
		sb.WriteString("### Pre-Review Summary\n")
		sb.WriteString(summary)
		sb.WriteString("\n\n")
		sb.WriteString(requestText)
		requestText = sb.String()

		if r.Config != nil && r.Config.Render == "acp" && !r.Config.WaitForMessage {
			requestText, err = r.waitAndAppendInitialUserContext(ctx, requestText)
			if err != nil {
				return "", err
			}
		}
	}

	if r.Config.PreReviewPromptFile != "" {
		r.Agent.Stage = "AI Code Review"
	} else {
		r.Agent.Stage = ""
	}
	r.Agent.ResetMetrics()
	maxIterations := CalculateMaxIterations(len(changedFiles))
	reviewResult, err := r.Agent.RunReview(ctx, r.StablePrompt, dynamic, requestText, maxIterations, r.Config.MaxTokens)
	reviewMetrics := r.Agent.GetMetrics()
	r.CodeReviewMetrics = &reviewMetrics
	if err != nil {
		return "", err
	}
	r.SetVerdict(DetermineVerdict(reviewResult))
	return reviewResult, nil
}

const postReviewSystemInstruction = "You have completed the automated code review. " +
	"You are now in an interactive post-review chat phase. " +
	"Transition purely into a conversational assistant. Answer the developer's questions, " +
	"defend your findings, or admit mistakes based on the user's input. " +
	"Do NOT attempt to rewrite the code review or output a new structured code review payload. " +
	"If the developer's explanations, answers, or code modifications resolve previous blocking issues " +
	"or introduce new blockers such that your overall review decision changes, you MUST call the " +
	"`update_review_verdict` tool to formally update your verdict ('approved', 'rejected', or 'comment') with your rationale."

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

func (r *Reviewer) executePostReviewTurn(ctx context.Context, cleanInput string, maxIterations int, spinnerWriter io.Writer) error {
	r.Agent.reporter.ReportPostReviewUserQuery(cleanInput)

	var spinner *terminalSpinner
	if r.Config.Render == "tui" && spinnerWriter != nil {
		spinner = newTerminalSpinner(spinnerWriter)
		spinner.Start("Cassandra is thinking...")
	}

	reply, err := r.Agent.RunChatFlight(ctx, cleanInput, maxIterations, r.Config.MaxTokens)
	if spinner != nil {
		spinner.Stop()
	}

	turnMetrics := r.Agent.GetMetrics()
	if r.DiscussionMetrics == nil {
		r.DiscussionMetrics = &turnMetrics
	} else {
		r.DiscussionMetrics.Add(turnMetrics)
	}
	r.Agent.ResetMetrics()

	if err != nil {
		return fmt.Errorf("chat flight failed: %w", err)
	}

	r.Agent.reporter.ReportPostReviewReply(reply)
	return nil
}

// RunInteractivePostReview starts a continuous chat loop with the developer.
func (r *Reviewer) RunInteractivePostReview(ctx context.Context) error {
	// Reset metrics to isolate the interactive post-review discussion phase
	r.Agent.ResetMetrics()

	// Append system instruction indicating conversational post-review phase if not already present
	if !slices.ContainsFunc(r.Agent.history, func(msg llm.Message) bool {
		return msg.Role == llm.RoleSystem && msg.Text == postReviewSystemInstruction
	}) {
		postMsg := llm.Message{
			Role: llm.RoleSystem,
			Text: postReviewSystemInstruction,
		}
		r.Agent.history = append(r.Agent.history, postMsg)
		if r.Agent.sessionStore != nil && r.Agent.sessionID != "" {
			if storeErr := r.Agent.sessionStore.AppendMessages(ctx, r.Agent.sessionID, postMsg); storeErr != nil {
				r.Agent.reporter.ReportWarning("Failed to persist post-review system instruction", storeErr)
			}
		}
	}

	maxIterations := CalculateMaxIterations(1)

	if server, ok := r.Agent.reporter.(PostReviewServer); ok {
		return server.ServePostReview(ctx, func(turnCtx context.Context, cleanInput string) error {
			return r.executePostReviewTurn(turnCtx, cleanInput, maxIterations, nil)
		})
	}
	if r.Config.Render == "acp" {
		return errors.New("ACP mode requires a reporter that implements PostReviewServer")
	}

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

		if err := r.executePostReviewTurn(replCtx, cleanInput, maxIterations, errWriter); err != nil {
			return err
		}
	}
}

// GetMetrics returns the captured metrics for both phases.
func (r *Reviewer) GetMetrics() ReviewMetrics {
	var phases []PhaseMetrics
	if r.PreReviewMetrics != nil {
		phases = append(phases, PhaseMetrics{
			Phase:   "pre_review",
			Metrics: *r.PreReviewMetrics,
		})
	}
	if r.CodeReviewMetrics != nil {
		phases = append(phases, PhaseMetrics{
			Phase:   "review",
			Metrics: *r.CodeReviewMetrics,
		})
	}
	if r.DiscussionMetrics != nil {
		phases = append(phases, PhaseMetrics{
			Phase:   "review_discussion",
			Metrics: *r.DiscussionMetrics,
		})
	}
	return ReviewMetrics{
		Phases: phases,
	}
}
