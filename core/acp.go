package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
)

const (
	defaultACPSessionID = acp.SessionId("sess_1")

	acpModeReview = acp.SessionModeId("review")
	acpModeChat   = acp.SessionModeId("chat")

	planStepContext   = "Gather git diff and commit context"
	planStepPreReview = "Generate pre-review summary"
	planStepReview    = "Inspect code and formulate review"
	planStepChat      = "Interactive post-review discussion"
)

var acpToolKinds = map[string]acp.ToolKind{
	"read_file":      acp.ToolKindRead,
	"list_directory": acp.ToolKindRead,
	"grep_files":     acp.ToolKindSearch,
	"glob_files":     acp.ToolKindSearch,
	"wishlist_tool":  acp.ToolKindOther,
}

func availableACPModes() []acp.SessionMode {
	reviewDesc := "Analyzing git diff, commit history, and repository context"
	chatDesc := "Discussing review findings and answering follow-up questions"
	return []acp.SessionMode{
		{
			Id:          acpModeReview,
			Name:        "Automated Code Review",
			Description: &reviewDesc,
		},
		{
			Id:          acpModeChat,
			Name:        "Interactive Post-Review Q&A",
			Description: &chatDesc,
		},
	}
}

// sessionGateWriter wraps the outbound ACP stream so that the sessionReady
// channel is closed immediately after the NewSessionResponse JSON-RPC frame is
// written, guaranteeing that subsequent session/update notifications never
// precede the session/new response on the wire.
type sessionGateWriter struct {
	mu          sync.Mutex
	out         io.Writer
	armOnNext   bool
	readySignal func()
}

func (w *sessionGateWriter) arm(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armOnNext = true
	w.readySignal = fn
}

func (w *sessionGateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.out.Write(p)
	var signal func()
	if w.armOnNext {
		w.armOnNext = false
		signal = w.readySignal
		w.readySignal = nil
	}
	w.mu.Unlock()

	if signal != nil {
		signal()
	}
	return n, err
}

// acpReporter implements both core.Reporter and acp.Agent, streaming initial
// review progress as ACP session/update notifications and serving interactive
// post-review chat turns over JSON-RPC 2.0 stdio.
type acpReporter struct {
	conn          *acp.AgentSideConnection
	gateWriter    *sessionGateWriter
	in            io.Reader
	stderr        io.Writer
	cancelSession context.CancelFunc

	mu           sync.Mutex
	sessionID    acp.SessionId
	currentMode  acp.SessionModeId
	sessionReady chan struct{}
	sessionOnce  sync.Once

	reviewReady chan struct{}
	reviewOnce  sync.Once

	promptHandler func(ctx context.Context, query string) error
	activeCancel  context.CancelFunc

	pendingTools map[string][]acp.ToolCallId
	toolSeq      int

	cfg             *config.Config
	targetDir       string
	hasPreReview    bool
	planStatuses    map[string]acp.PlanEntryStatus
	cumulativeUsage llm.Usage

	closeOnce sync.Once
}

var (
	_ Reporter  = (*acpReporter)(nil)
	_ acp.Agent = (*acpReporter)(nil)
)

// NewACPReporter creates a Reporter backed by an ACP AgentSideConnection over
// stdout and stdin. All diagnostic warnings and internal connection logs are
// routed to stderr so stdout remains reserved exclusively for JSON-RPC frames.
func NewACPReporter(stdout io.Writer, stdin io.Reader, stderr io.Writer, cancelSession context.CancelFunc) Reporter {
	if stderr == nil {
		stderr = io.Discard
	}
	gate := &sessionGateWriter{out: stdout}
	r := &acpReporter{
		gateWriter:    gate,
		in:            stdin,
		stderr:        stderr,
		cancelSession: cancelSession,
		sessionID:     defaultACPSessionID,
		currentMode:   acpModeReview,
		sessionReady:  make(chan struct{}),
		reviewReady:   make(chan struct{}),
		pendingTools:  make(map[string][]acp.ToolCallId),
		planStatuses: map[string]acp.PlanEntryStatus{
			planStepContext:   acp.PlanEntryStatusPending,
			planStepPreReview: acp.PlanEntryStatusPending,
			planStepReview:    acp.PlanEntryStatusPending,
			planStepChat:      acp.PlanEntryStatusPending,
		},
	}

	conn := acp.NewAgentSideConnection(r, gate, stdin)
	conn.SetLogger(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))
	r.conn = conn

	return r
}

// Close terminates the ACP reporter and waits for the underlying connection loop
// to finish when reading from a closable non-stdin stream (such as test pipes).
func (r *acpReporter) Close() error {
	r.closeOnce.Do(func() {
		r.cancelActiveTurn()
		r.signalSessionReady()
		r.signalReviewReady()
		if closer, ok := r.in.(io.Closer); ok && r.in != os.Stdin {
			_ = closer.Close()
			<-r.conn.Done()
		}
	})
	return nil
}

func (r *acpReporter) signalSessionReady() {
	r.sessionOnce.Do(func() {
		close(r.sessionReady)
	})
}

func (r *acpReporter) signalReviewReady() {
	r.reviewOnce.Do(func() {
		close(r.reviewReady)
	})
}

func (r *acpReporter) cancelActiveTurn() {
	r.mu.Lock()
	cancel := r.activeCancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ServePostReview registers the chat turn handler, transitions the session mode
// and execution plan to the interactive post-review phase, unlocks any pending
// or future ACP session/prompt requests, and blocks until the context is
// canceled or the ACP client disconnects.
func (r *acpReporter) ServePostReview(ctx context.Context, handler func(ctx context.Context, query string) error) error {
	r.mu.Lock()
	r.promptHandler = handler
	r.mu.Unlock()

	r.emitModeUpdate(acpModeChat)
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext:   acp.PlanEntryStatusCompleted,
		planStepPreReview: acp.PlanEntryStatusCompleted,
		planStepReview:    acp.PlanEntryStatusCompleted,
		planStepChat:      acp.PlanEntryStatusInProgress,
	})

	r.signalReviewReady()

	select {
	case <-ctx.Done():
		return nil
	case <-r.conn.Done():
		return nil
	}
}

func (r *acpReporter) sendUpdate(update acp.SessionUpdate) {
	select {
	case <-r.sessionReady:
	case <-r.conn.Done():
		return
	}

	r.mu.Lock()
	sid := r.sessionID
	r.mu.Unlock()

	_ = r.conn.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: sid,
		Update:    update,
	})
}

func (r *acpReporter) emitModeUpdate(mode acp.SessionModeId) {
	r.mu.Lock()
	if r.currentMode == mode {
		r.mu.Unlock()
		return
	}
	r.currentMode = mode
	r.mu.Unlock()

	r.sendUpdate(acp.SessionUpdate{
		CurrentModeUpdate: &acp.SessionCurrentModeUpdate{
			CurrentModeId: mode,
		},
	})
}

func (r *acpReporter) emitSessionInfo(title string, meta map[string]any) {
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	r.sendUpdate(acp.SessionUpdate{
		SessionInfoUpdate: &acp.SessionSessionInfoUpdate{
			Title:     &title,
			UpdatedAt: &updatedAt,
			Meta:      meta,
		},
	})
}

func (r *acpReporter) advancePlan(updates map[string]acp.PlanEntryStatus) {
	r.mu.Lock()
	changed := false
	for step, status := range updates {
		if r.planStatuses[step] != status {
			r.planStatuses[step] = status
			changed = true
		}
	}
	if !changed {
		r.mu.Unlock()
		return
	}

	entries := []acp.PlanEntry{
		{
			Content:  planStepContext,
			Priority: acp.PlanEntryPriorityHigh,
			Status:   r.planStatuses[planStepContext],
		},
	}
	if r.hasPreReview {
		entries = append(entries, acp.PlanEntry{
			Content:  planStepPreReview,
			Priority: acp.PlanEntryPriorityMedium,
			Status:   r.planStatuses[planStepPreReview],
		})
	}
	entries = append(
		entries,
		acp.PlanEntry{
			Content:  planStepReview,
			Priority: acp.PlanEntryPriorityHigh,
			Status:   r.planStatuses[planStepReview],
		},
		acp.PlanEntry{
			Content:  planStepChat,
			Priority: acp.PlanEntryPriorityMedium,
			Status:   r.planStatuses[planStepChat],
		},
	)
	r.mu.Unlock()

	r.sendUpdate(acp.UpdatePlan(entries...))
}

func (r *acpReporter) writeStderr(format string, args ...any) {
	fmt.Fprintf(r.stderr, format, args...)
}

// --- acp.Agent implementation ---

func (r *acpReporter) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	title := "Cassandra AI Code Reviewer"
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo: &acp.Implementation{
			Name:    "cassandra",
			Title:   &title,
			Version: "0.0.1",
		},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: false,
			SessionCapabilities: acp.SessionCapabilities{
				Close: &acp.SessionCloseCapabilities{},
			},
		},
		AuthMethods: []acp.AuthMethod{},
	}, nil
}

func (r *acpReporter) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (r *acpReporter) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (r *acpReporter) NewSession(_ context.Context, _ acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	r.mu.Lock()
	sid := r.sessionID
	mode := r.currentMode
	r.mu.Unlock()

	r.gateWriter.arm(r.signalSessionReady)
	return acp.NewSessionResponse{
		SessionId: sid,
		Modes: &acp.SessionModeState{
			CurrentModeId:  mode,
			AvailableModes: availableACPModes(),
		},
	}, nil
}

func (r *acpReporter) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	if err := ctx.Err(); err != nil {
		return acp.PromptResponse{}, err
	}

	// Wait for the initial review to complete and the post-review handler to be ready.
	select {
	case <-r.reviewReady:
	case <-ctx.Done():
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case <-r.conn.Done():
		return acp.PromptResponse{}, errors.New("ACP connection closed")
	}

	query := extractPromptText(params.Prompt)
	cleanInput := strings.TrimSpace(query)
	if cleanInput == "" {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	r.mu.Lock()
	handler := r.promptHandler
	turnCtx, turnCancel := context.WithCancel(ctx)
	r.activeCancel = turnCancel
	r.mu.Unlock()

	defer func() {
		turnCancel()
		r.mu.Lock()
		r.activeCancel = nil
		r.mu.Unlock()
	}()

	if handler == nil {
		return acp.PromptResponse{}, errors.New("post-review chat handler is not available")
	}

	if err := handler(turnCtx, cleanInput); err != nil {
		if errors.Is(err, context.Canceled) || turnCtx.Err() != nil {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}

	return acp.PromptResponse{
		StopReason: acp.StopReasonEndTurn,
	}, nil
}

func (r *acpReporter) Cancel(_ context.Context, _ acp.CancelNotification) error {
	r.cancelActiveTurn()
	return nil
}

func (r *acpReporter) CloseSession(_ context.Context, _ acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	r.cancelActiveTurn()
	if r.cancelSession != nil {
		go r.cancelSession()
	}
	return acp.CloseSessionResponse{}, nil
}

func (r *acpReporter) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (r *acpReporter) ResumeSession(_ context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (r *acpReporter) SetSessionConfigOption(_ context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (r *acpReporter) SetSessionMode(_ context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	r.emitModeUpdate(req.ModeId)
	return acp.SetSessionModeResponse{}, nil
}

func extractPromptText(blocks []acp.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Text != nil && b.Text.Text != "" {
			parts = append(parts, b.Text.Text)
		} else if b.Resource != nil && b.Resource.Resource.TextResourceContents != nil {
			parts = append(parts, b.Resource.Resource.TextResourceContents.Text)
		} else if b.ResourceLink != nil && b.ResourceLink.Uri != "" {
			parts = append(parts, b.ResourceLink.Uri)
		}
	}
	return strings.Join(parts, "\n")
}

// --- core.Reporter implementation ---

func (r *acpReporter) ReportIteration(iter int) {
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext:   acp.PlanEntryStatusCompleted,
		planStepPreReview: acp.PlanEntryStatusCompleted,
		planStepReview:    acp.PlanEntryStatusInProgress,
	})
	r.sendUpdate(acp.UpdateAgentThoughtText(fmt.Sprintf("Review iteration %d...\n", iter)))
}

func (r *acpReporter) ReportStageIteration(stage string, iter int) {
	if strings.EqualFold(stage, "Pre-Review Summary") {
		r.advancePlan(map[string]acp.PlanEntryStatus{
			planStepContext:   acp.PlanEntryStatusCompleted,
			planStepPreReview: acp.PlanEntryStatusInProgress,
		})
	} else {
		r.advancePlan(map[string]acp.PlanEntryStatus{
			planStepContext:   acp.PlanEntryStatusCompleted,
			planStepPreReview: acp.PlanEntryStatusCompleted,
			planStepReview:    acp.PlanEntryStatusInProgress,
		})
	}
	r.sendUpdate(acp.UpdateAgentThoughtText(fmt.Sprintf("[%s] Iteration %d...\n", stage, iter)))
}

func (r *acpReporter) ReportToolCalls(tcs []llm.ToolCall) {
	for _, tc := range tcs {
		if tc.Name == "emit_reviewer_state" {
			var args struct {
				Message   string `json:"message"`
				FocusArea string `json:"focus_area"`
			}
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
			if args.Message != "" {
				thought := args.Message
				if args.FocusArea != "" {
					thought = fmt.Sprintf("[%s] %s", args.FocusArea, args.Message)
				}
				r.sendUpdate(acp.UpdateAgentThoughtText(thought + "\n"))
			}
			continue
		}

		r.mu.Lock()
		r.toolSeq++
		callID := acp.ToolCallId(tc.ID)
		if callID == "" {
			callID = acp.ToolCallId(fmt.Sprintf("call_%d", r.toolSeq))
		}
		r.pendingTools[tc.Name] = append(r.pendingTools[tc.Name], callID)
		r.mu.Unlock()

		kind, known := acpToolKinds[tc.Name]
		if !known {
			kind = acp.ToolKindOther
		}

		title := tc.Name
		if compact := compactToolCallArgs(tc); compact != "" {
			title = fmt.Sprintf("%s(%s)", tc.Name, compact)
		}

		opts := []acp.ToolCallStartOpt{
			acp.WithStartKind(kind),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
		}

		var rawArgs map[string]any
		if err := json.Unmarshal([]byte(tc.Arguments), &rawArgs); err == nil && len(rawArgs) > 0 {
			opts = append(opts, acp.WithStartRawInput(rawArgs))
			if path, ok := rawArgs["file_path"].(string); ok && path != "" {
				opts = append(opts, acp.WithStartLocations([]acp.ToolCallLocation{{Path: path}}))
			} else if path, ok := rawArgs["path"].(string); ok && path != "" {
				opts = append(opts, acp.WithStartLocations([]acp.ToolCallLocation{{Path: path}}))
			}
		}

		r.sendUpdate(acp.StartToolCall(callID, title, opts...))
	}
}

func (r *acpReporter) ReportToolStatus(name string, status string, err error) {
	if status == "started" {
		return
	}

	r.mu.Lock()
	queue := r.pendingTools[name]
	var callID acp.ToolCallId
	if len(queue) > 0 {
		callID = queue[0]
		r.pendingTools[name] = queue[1:]
	}
	r.mu.Unlock()

	if callID == "" {
		return
	}

	acpStatus := acp.ToolCallStatusCompleted
	opts := []acp.ToolCallUpdateOpt{
		acp.WithUpdateStatus(acpStatus),
	}
	if status == "failed" || err != nil {
		opts = []acp.ToolCallUpdateOpt{
			acp.WithUpdateStatus(acp.ToolCallStatusFailed),
		}
		if err != nil {
			opts = append(opts, acp.WithUpdateRawOutput(err.Error()))
		}
	}

	r.sendUpdate(acp.UpdateToolCall(callID, opts...))
}

func (r *acpReporter) ReportUsage(usage llm.Usage) {
	if usage.PromptTokens < 0 || usage.OutputTokens < 0 {
		return
	}
	if usage.PromptTokens == 0 && usage.OutputTokens == 0 && usage.ThinkingTokens == 0 && usage.CachedTokens == 0 {
		return
	}

	r.mu.Lock()
	r.cumulativeUsage.Add(usage)
	cumIn := r.cumulativeUsage.TotalInput()
	cumOut := r.cumulativeUsage.TotalOutput()
	maxTokens := 0
	if r.cfg != nil {
		maxTokens = r.cfg.MaxTokens
	}
	r.mu.Unlock()

	used := usage.TotalInput() + usage.TotalOutput()
	size := maxTokens
	if size < used {
		size = used
	}

	r.sendUpdate(acp.SessionUpdate{
		UsageUpdate: &acp.SessionUsageUpdate{
			Used: used,
			Size: size,
			Meta: map[string]any{
				"turnInputTokens":        usage.PromptTokens,
				"turnOutputTokens":       usage.OutputTokens,
				"turnThinkingTokens":     usage.ThinkingTokens,
				"turnCachedTokens":       usage.CachedTokens,
				"cumulativeInputTokens":  cumIn,
				"cumulativeOutputTokens": cumOut,
			},
		},
	})
}

func (r *acpReporter) ReportUsageSummary(_ llm.Usage) {}

func (r *acpReporter) ReportFinalReview() {
	r.sendUpdate(acp.UpdateAgentThoughtText("Formulating final review...\n"))
}

func (r *acpReporter) ReportStageFinalReview(stage string) {
	r.sendUpdate(acp.UpdateAgentThoughtText(fmt.Sprintf("Formulating final %s...\n", strings.ToLower(stage))))
}

func (r *acpReporter) ReportExtraction() {
	r.sendUpdate(acp.UpdateAgentThoughtText("Extracting structured findings...\n"))
}

func (r *acpReporter) ReportExtractionRetry(attempt int) {
	r.writeStderr("[Retry] Extraction attempt %d failed; retrying...\n", attempt)
}

func (r *acpReporter) ReportEmptyResponseRetry(attempt int) {
	r.writeStderr("[Retry] LLM returned empty response (attempt %d); retrying...\n", attempt)
}

func (r *acpReporter) ReportCapReached(maxIterations int) {
	r.writeStderr("Reached maximum ReAct iterations (%d). Forcing final review.\n", maxIterations)
}

func (r *acpReporter) ReportTruncated(maxTokens int) {
	r.writeStderr("LLM response truncated (hit max-tokens limit of %d).\n", maxTokens)
}

func (r *acpReporter) ReportMCPStatus(name string, status string, err error) {
	if err != nil {
		r.writeStderr("[MCP] %s: %s: %v\n", name, status, err)
		return
	}
	r.sendUpdate(acp.UpdateAgentThoughtText(fmt.Sprintf("[MCP] %s: %s\n", name, status)))
}

func (r *acpReporter) ReportReviewHeader(files int, guidelines string, model string) {
	r.mu.Lock()
	cfg := r.cfg
	targetDir := r.targetDir
	r.mu.Unlock()

	title := fmt.Sprintf("Code Review (%d files, %s)", files, model)
	meta := map[string]any{
		"changedFiles": files,
		"guidelines":   guidelines,
		"model":        model,
	}
	if targetDir != "" {
		meta["targetDir"] = targetDir
	}
	if cfg != nil {
		if cfg.Provider != "" {
			meta["provider"] = cfg.Provider
		}
		if cfg.Base != "" && cfg.Head != "" {
			title = fmt.Sprintf("Code Review: %s..%s (%d files, %s)", cfg.Base, cfg.Head, files, model)
			meta["base"] = cfg.Base
			meta["head"] = cfg.Head
		}
	}

	r.emitSessionInfo(title, meta)
}

func (r *acpReporter) ReportConfig(cfg *config.Config, targetDir string) {
	if cfg == nil {
		return
	}

	r.mu.Lock()
	r.cfg = cfg
	r.targetDir = targetDir
	r.hasPreReview = cfg.PreReviewPromptFile != ""
	r.mu.Unlock()

	title := fmt.Sprintf("Code Review (%s)", cfg.Model)
	meta := map[string]any{
		"provider":   cfg.Provider,
		"model":      cfg.Model,
		"guidelines": cfg.MainGuidelines,
	}
	if targetDir != "" {
		meta["targetDir"] = targetDir
	}
	if cfg.Base != "" && cfg.Head != "" {
		title = fmt.Sprintf("Code Review: %s..%s (%s)", cfg.Base, cfg.Head, cfg.Model)
		meta["base"] = cfg.Base
		meta["head"] = cfg.Head
	}

	r.emitSessionInfo(title, meta)
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext: acp.PlanEntryStatusInProgress,
	})
	r.sendUpdate(acp.UpdateAgentThoughtText(fmt.Sprintf("Configured reviewer (%s / %s)\n", cfg.Provider, cfg.Model)))
}

func (r *acpReporter) ReportFetchingDiff() {
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext: acp.PlanEntryStatusInProgress,
	})
	r.sendUpdate(acp.UpdateAgentThoughtText("Fetching git diff...\n"))
}

func (r *acpReporter) ReportFetchingCommits() {
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext: acp.PlanEntryStatusInProgress,
	})
	r.sendUpdate(acp.UpdateAgentThoughtText("Fetching git commits...\n"))
}

func (r *acpReporter) ReportNoChanges() {
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext: acp.PlanEntryStatusCompleted,
		planStepReview:  acp.PlanEntryStatusCompleted,
	})
	r.sendUpdate(acp.UpdateAgentMessageText("No changes found to review."))
	r.signalReviewReady()
}

func (r *acpReporter) ReportReview(result string) error {
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext:   acp.PlanEntryStatusCompleted,
		planStepPreReview: acp.PlanEntryStatusCompleted,
		planStepReview:    acp.PlanEntryStatusCompleted,
	})
	if result != "" {
		r.sendUpdate(acp.UpdateAgentMessageText(result))
	}
	return nil
}

func (r *acpReporter) ReportStageReview(stage, result string) error {
	if strings.EqualFold(stage, "Pre-Review Summary") {
		r.advancePlan(map[string]acp.PlanEntryStatus{
			planStepContext:   acp.PlanEntryStatusCompleted,
			planStepPreReview: acp.PlanEntryStatusCompleted,
		})
	}
	if result != "" {
		r.sendUpdate(acp.UpdateAgentMessageText(fmt.Sprintf("# %s\n\n%s\n\n", stage, result)))
	}
	return nil
}

func (r *acpReporter) ReportReviewWritten(file string) {
	r.writeStderr("Review written to %s\n", file)
}

func (r *acpReporter) ReportStructuredReviewWritten(file string) {
	r.writeStderr("Structured review written to %s\n", file)
}

func (r *acpReporter) ReportMetricsWritten(file string) {
	r.writeStderr("Metrics written to %s\n", file)
}

func (r *acpReporter) ReportWarning(msg string, err error) {
	if err != nil {
		r.writeStderr("Warning: %s: %v\n", msg, err)
		return
	}
	r.writeStderr("Warning: %s\n", msg)
}

func (r *acpReporter) ReportError(err error) {
	if err != nil {
		r.writeStderr("Error: %v\n", err)
	}
}

func (r *acpReporter) NotifyUser() {}

func (r *acpReporter) ReportPostReviewReply(message string) {
	if message != "" {
		r.sendUpdate(acp.UpdateAgentMessageText(message))
	}
}

func (r *acpReporter) ReportPostReviewUserQuery(_ string) {}
