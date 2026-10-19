package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

	planStepContext   = "Gather git diff and commit context"
	planStepPreReview = "Generate pre-review summary"
	planStepReview    = "Inspect code and formulate review"
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
	return []acp.SessionMode{
		{
			Id:          acpModeReview,
			Name:        "Code review",
			Description: &reviewDesc,
		},
	}
}

func acpConfigOptions(modelName string) []acp.SessionConfigOption {
	if modelName == "" {
		return nil
	}
	category := acp.SessionConfigOptionCategoryModel
	val := acp.SessionConfigValueId(modelName)
	ungrouped := acp.SessionConfigSelectOptionsUngrouped{
		{
			Value: val,
			Name:  modelName,
		},
	}
	return []acp.SessionConfigOption{
		{
			Select: &acp.SessionConfigOptionSelect{
				Id:           acp.SessionConfigId("model"),
				Name:         "Model",
				Category:     &category,
				CurrentValue: val,
				Options: acp.SessionConfigSelectOptions{
					Ungrouped: &ungrouped,
				},
				Type: "select",
			},
		},
	}
}

// sessionGateWriter wraps the outbound ACP stream so that registered callbacks
// (such as closing sessionReady after NewSessionResponse or triggering session
// cancellation after CloseSessionResponse) are invoked immediately after the
// next JSON-RPC frame is written.
type sessionGateWriter struct {
	mu           sync.Mutex
	out          io.Writer
	readySignals []func()
}

func (w *sessionGateWriter) arm(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.readySignals = append(w.readySignals, fn)
}

func (w *sessionGateWriter) flushSignals() {
	w.mu.Lock()
	signals := w.readySignals
	w.readySignals = nil
	w.mu.Unlock()

	for _, fn := range signals {
		if fn != nil {
			fn()
		}
	}
}

func (w *sessionGateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.out.Write(p)
	signals := w.readySignals
	w.readySignals = nil
	w.mu.Unlock()

	for _, fn := range signals {
		if fn != nil {
			fn()
		}
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
	inPostReview bool
	sessionReady chan struct{}
	sessionOnce  sync.Once

	reviewReady chan struct{}
	reviewOnce  sync.Once

	firstMessageReady   chan struct{}
	firstMessageOnce    sync.Once
	firstMessageText    string
	waitForMessageArmed bool

	stagedReviews []string

	promptHandler func(ctx context.Context, query string) error
	activeCancel  context.CancelFunc

	pendingCallsByID map[string]acp.ToolCallId
	pendingByName    map[string][]acp.ToolCallId
	toolSeq          int
	msgSeq           int

	// ctx bridges core.Reporter methods (which do not accept a context.Context)
	// to outbound conn.SessionUpdate notifications.
	ctx              context.Context
	closeSessionOnce sync.Once
	shutdownWg       sync.WaitGroup

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
func NewACPReporter(ctx context.Context, stdout io.Writer, stdin io.Reader, stderr io.Writer, cancelSession context.CancelFunc) Reporter {
	if ctx == nil {
		ctx = context.Background()
	}
	if stderr == nil {
		stderr = io.Discard
	}
	gate := &sessionGateWriter{out: stdout}
	r := &acpReporter{
		ctx:               ctx,
		gateWriter:        gate,
		in:                stdin,
		stderr:            stderr,
		cancelSession:     cancelSession,
		sessionID:         defaultACPSessionID,
		currentMode:       acpModeReview,
		sessionReady:      make(chan struct{}),
		reviewReady:       make(chan struct{}),
		firstMessageReady: make(chan struct{}),
		pendingCallsByID:  make(map[string]acp.ToolCallId),
		pendingByName:     make(map[string][]acp.ToolCallId),
		planStatuses: map[string]acp.PlanEntryStatus{
			planStepContext:   acp.PlanEntryStatusPending,
			planStepPreReview: acp.PlanEntryStatusPending,
			planStepReview:    acp.PlanEntryStatusPending,
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
		r.signalFirstMessage("")
		if closer, ok := r.in.(io.Closer); ok && r.in != os.Stdin {
			_ = closer.Close()
			<-r.conn.Done()
		}
		r.gateWriter.flushSignals()
		r.shutdownWg.Wait()
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

func (r *acpReporter) signalFirstMessage(text string) bool {
	isFirst := false
	r.firstMessageOnce.Do(func() {
		isFirst = true
		r.mu.Lock()
		r.firstMessageText = text
		r.mu.Unlock()
		close(r.firstMessageReady)
	})
	return isFirst
}

// WaitForFirstMessage blocks until the first ACP session/prompt request is
// received, the context is canceled, or the ACP connection closes, and returns
// the text of that initial user message.
func (r *acpReporter) WaitForFirstMessage(ctx context.Context) (string, error) {
	r.mu.Lock()
	r.waitForMessageArmed = true
	r.mu.Unlock()

	select {
	case <-r.firstMessageReady:
		r.mu.Lock()
		text := r.firstMessageText
		r.mu.Unlock()
		return text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.ctx.Done():
		return "", r.ctx.Err()
	case <-r.conn.Done():
		return "", errors.New("ACP connection closed")
	}
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
	r.inPostReview = true
	r.mu.Unlock()

	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext:   acp.PlanEntryStatusCompleted,
		planStepPreReview: acp.PlanEntryStatusCompleted,
		planStepReview:    acp.PlanEntryStatusCompleted,
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
	case <-r.ctx.Done():
		return
	}

	r.mu.Lock()
	sid := r.sessionID
	r.mu.Unlock()

	_ = r.conn.SessionUpdate(r.ctx, acp.SessionNotification{
		SessionId: sid,
		Update:    update,
	})
}

func (r *acpReporter) sendAgentMessage(text string) {
	if text == "" {
		return
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n\n"
	} else if !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}

	r.mu.Lock()
	r.msgSeq++
	msgID := fmt.Sprintf("00000000-0000-4000-8000-%012x", r.msgSeq)
	r.mu.Unlock()

	r.sendUpdate(acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content:   acp.TextBlock(text),
			MessageId: &msgID,
		},
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
	var modelName string
	if r.cfg != nil {
		modelName = r.cfg.Model
	}
	r.mu.Unlock()

	r.gateWriter.arm(r.signalSessionReady)
	return acp.NewSessionResponse{
		SessionId:     sid,
		ConfigOptions: acpConfigOptions(modelName),
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

	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()

	r.mu.Lock()
	if r.activeCancel != nil {
		r.mu.Unlock()
		return acp.PromptResponse{}, errors.New("concurrent prompt turns are not allowed on the same session")
	}
	r.activeCancel = turnCancel
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.activeCancel = nil
		r.mu.Unlock()
	}()

	query := extractPromptText(params.Prompt)
	cleanInput := strings.TrimSpace(query)
	isFirstMessage := r.signalFirstMessage(cleanInput)

	// Wait for the initial review to complete and the post-review handler to be ready.
	select {
	case <-r.reviewReady:
	case <-turnCtx.Done():
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case <-r.conn.Done():
		return acp.PromptResponse{}, errors.New("ACP connection closed")
	}

	r.mu.Lock()
	waitForMessage := r.waitForMessageArmed
	handler := r.promptHandler
	r.mu.Unlock()

	// When the initial review waited for a message (via --wait-for-message or
	// pre-review focus), the first user message triggered the review (and was
	// injected into the review prompt as extra user context), so the turn
	// completes as soon as the review is emitted.
	if isFirstMessage && waitForMessage {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	if cleanInput == "" {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

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
	r.closeSessionOnce.Do(func() {
		if r.cancelSession != nil {
			r.shutdownWg.Add(1)
			r.gateWriter.arm(func() {
				go func() {
					defer r.shutdownWg.Done()
					r.cancelSession()
				}()
			})
		}
	})
	return acp.CloseSessionResponse{}, nil
}

func (r *acpReporter) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (r *acpReporter) ResumeSession(_ context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (r *acpReporter) SetSessionConfigOption(_ context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	r.mu.Lock()
	var modelName string
	if r.cfg != nil {
		modelName = r.cfg.Model
	}
	r.mu.Unlock()
	return acp.SetSessionConfigOptionResponse{
		ConfigOptions: acpConfigOptions(modelName),
	}, nil
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
	r.mu.Lock()
	isChat := r.inPostReview
	r.mu.Unlock()

	if isChat {
		r.sendUpdate(acp.UpdateAgentThoughtText(fmt.Sprintf("Thinking (iteration %d)...\n", iter)))
		return
	}

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
		if focusArea, message, ok := parseReviewerState(tc); ok {
			if message != "" {
				thought := message
				if focusArea != "" {
					thought = fmt.Sprintf("[%s] %s", focusArea, message)
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
		if tc.ID != "" {
			r.pendingCallsByID[tc.ID] = callID
		}
		r.pendingByName[tc.Name] = append(r.pendingByName[tc.Name], callID)
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
			path, _ := rawArgs["file_path"].(string)
			if path == "" {
				path, _ = rawArgs["path"].(string)
			}
			if path != "" {
				opts = append(opts, acp.WithStartLocations([]acp.ToolCallLocation{
					r.buildToolCallLocation(path, rawArgs),
				}))
			}
		}

		r.sendUpdate(acp.StartToolCall(callID, title, opts...))
	}
}

func (r *acpReporter) ReportToolStatus(tc llm.ToolCall, status string, err error) {
	if status == "started" {
		return
	}

	r.mu.Lock()
	var callID acp.ToolCallId
	if tc.ID != "" {
		if id, ok := r.pendingCallsByID[tc.ID]; ok {
			callID = id
			delete(r.pendingCallsByID, tc.ID)
			if idx := slices.Index(r.pendingByName[tc.Name], callID); idx >= 0 {
				r.pendingByName[tc.Name] = slices.Delete(r.pendingByName[tc.Name], idx, idx+1)
			}
		}
	}
	if callID == "" {
		if queue := r.pendingByName[tc.Name]; len(queue) > 0 {
			callID = queue[0]
			r.pendingByName[tc.Name] = queue[1:]
			for idKey, cid := range r.pendingCallsByID {
				if cid == callID {
					delete(r.pendingCallsByID, idKey)
					break
				}
			}
		}
	}
	if len(r.pendingByName[tc.Name]) == 0 {
		delete(r.pendingByName, tc.Name)
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
	size := max(maxTokens, used)

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

func (r *acpReporter) buildSessionInfo(files int, guidelines, model string) (string, map[string]any) {
	r.mu.Lock()
	cfg := r.cfg
	targetDir := r.targetDir
	r.mu.Unlock()

	meta := map[string]any{
		"guidelines": guidelines,
		"model":      model,
	}
	detail := model
	if files >= 0 {
		meta["changedFiles"] = files
		detail = fmt.Sprintf("%d files, %s", files, model)
	}
	if targetDir != "" {
		meta["targetDir"] = targetDir
	}

	title := fmt.Sprintf("Code Review (%s)", detail)
	if cfg != nil {
		if cfg.Provider != "" {
			meta["provider"] = cfg.Provider
		}
		if cfg.Base != "" && cfg.Head != "" {
			title = fmt.Sprintf("Code Review: %s..%s (%s)", cfg.Base, cfg.Head, detail)
			meta["base"] = cfg.Base
			meta["head"] = cfg.Head
		}
	}
	return title, meta
}

func (r *acpReporter) ReportReviewHeader(files int, guidelines string, model string) {
	title, meta := r.buildSessionInfo(files, guidelines, model)
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

	title, meta := r.buildSessionInfo(-1, cfg.MainGuidelines, cfg.Model)
	r.emitSessionInfo(title, meta)
	if opts := acpConfigOptions(cfg.Model); len(opts) > 0 {
		r.sendUpdate(acp.SessionUpdate{
			ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
				ConfigOptions: opts,
			},
		})
	}
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
	r.sendAgentMessage("No changes found to review.")
}

func (r *acpReporter) ReportReview(result string) error {
	r.advancePlan(map[string]acp.PlanEntryStatus{
		planStepContext:   acp.PlanEntryStatusCompleted,
		planStepPreReview: acp.PlanEntryStatusCompleted,
		planStepReview:    acp.PlanEntryStatusCompleted,
	})

	r.mu.Lock()
	staged := r.stagedReviews
	r.stagedReviews = nil
	r.mu.Unlock()

	formattedReview := r.formatMessageText(result)
	if len(staged) > 0 {
		var parts []string
		parts = append(parts, staged...)
		if formattedReview != "" {
			parts = append(parts, fmt.Sprintf("# Code Review\n\n%s", formattedReview))
		}
		r.sendAgentMessage(strings.Join(parts, "\n\n---\n\n"))
		return nil
	}

	if formattedReview != "" {
		r.sendAgentMessage(formattedReview)
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
		formatted := strings.TrimSpace(r.formatMessageText(result))
		r.mu.Lock()
		waitForMessage := r.cfg != nil && r.cfg.WaitForMessage
		if waitForMessage {
			r.stagedReviews = append(r.stagedReviews, fmt.Sprintf("# %s\n\n%s", stage, formatted))
		}
		r.mu.Unlock()

		if !waitForMessage {
			msg := fmt.Sprintf("# %s\n\n%s", stage, formatted)
			if strings.EqualFold(stage, "Pre-Review Summary") {
				msg += "\n\nIs there anything you want me to focus on in this review?"
			}
			r.sendAgentMessage(msg)
		}
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
		r.sendAgentMessage(r.formatMessageText(message))
	}
}

func (r *acpReporter) ReportPostReviewUserQuery(_ string) {}

func (r *acpReporter) resolveFilePath(path string) string {
	r.mu.Lock()
	targetDir := r.targetDir
	r.mu.Unlock()

	if targetDir != "" && !filepath.IsAbs(path) {
		return filepath.Join(targetDir, path)
	}
	return path
}

func (r *acpReporter) buildToolCallLocation(path string, rawArgs map[string]any) acp.ToolCallLocation {
	loc := acp.ToolCallLocation{
		Path: r.resolveFilePath(path),
	}
	for _, key := range []string{"line_start", "start_line", "line"} {
		switch v := rawArgs[key].(type) {
		case float64:
			if line := int(v); line > 0 {
				loc.Line = &line
				return loc
			}
		case int:
			if v > 0 {
				line := v
				loc.Line = &line
				return loc
			}
		}
	}
	return loc
}

var (
	fencedCodeBlockPattern  = regexp.MustCompile("(?s)```.*?```")
	backtickLabelRefPattern = regexp.MustCompile("`(\\[[^\\]\\n]+\\])\\s+\\[([^\\]\\n]+)\\]`")
	backtickBracketPattern  = regexp.MustCompile("`\\[([^\\]\\n]+)\\]`")
	backtickBareRefPattern  = regexp.MustCompile("`([A-Za-z0-9._/-]+:\\d+(?:-\\d+)?)`")
	bracketFileRefPattern   = regexp.MustCompile(`\[([A-Za-z0-9._/-]+:\d+(?:-\d+)?)\]`)
)

func (r *acpReporter) formatMessageText(text string) string {
	r.mu.Lock()
	targetDir := r.targetDir
	r.mu.Unlock()
	return linkifyFileReferences(text, targetDir)
}

// linkifyFileReferences converts Cassandra file+line references such as
// [path/to/file.go:42], [path/to/file.go:10-20], or `path/to/file.go:10-20`
// outside fenced code blocks into Markdown file links so ACP clients render
// clickable editor links.
func linkifyFileReferences(text, targetDir string) string {
	var sb strings.Builder
	last := 0
	for _, loc := range fencedCodeBlockPattern.FindAllStringIndex(text, -1) {
		sb.WriteString(linkifySegment(text[last:loc[0]], targetDir))
		sb.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	sb.WriteString(linkifySegment(text[last:], targetDir))
	return sb.String()
}

func linkifySegment(segment, targetDir string) string {
	// Unwrap `[label] [path:line]` into `[label]` [path:line]
	segment = backtickLabelRefPattern.ReplaceAllString(segment, "`$1` [$2]")
	// Unwrap `[path:line]` into [path:line] when it matches a file reference
	segment = backtickBracketPattern.ReplaceAllStringFunc(segment, func(m string) string {
		inner := m[2 : len(m)-2]
		if _, _, _, ok := parseFileLineRef(inner); ok {
			return "[" + inner + "]"
		}
		return m
	})
	// Convert `path:line` or `path:start-end` into [path:line] so it gets linkified
	segment = backtickBareRefPattern.ReplaceAllStringFunc(segment, func(m string) string {
		inner := m[1 : len(m)-1]
		if _, _, _, ok := parseFileLineRef(inner); ok {
			return "[" + inner + "]"
		}
		return m
	})

	matches := bracketFileRefPattern.FindAllStringSubmatchIndex(segment, -1)
	if len(matches) == 0 {
		return segment
	}

	var sb strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		sb.WriteString(segment[last:start])
		// Skip if already part of a Markdown link [label](href)
		if end < len(segment) && segment[end] == '(' {
			sb.WriteString(segment[start:end])
			last = end
			continue
		}
		inner := segment[m[2]:m[3]]
		filePath, linePart, line, ok := parseFileLineRef(inner)
		if !ok {
			sb.WriteString(segment[start:end])
			last = end
			continue
		}
		destPath := filePath
		if targetDir != "" && !filepath.IsAbs(filePath) {
			destPath = filepath.Join(targetDir, filePath)
		}
		destPath = filepath.ToSlash(destPath)
		labelPath := filepath.ToSlash(filePath)
		fmt.Fprintf(&sb, "[%s:%s](%s#L%d)", labelPath, linePart, destPath, line)
		last = end
	}
	sb.WriteString(segment[last:])
	return sb.String()
}

func parseFileLineRef(ref string) (filePath, linePart string, startLine int, ok bool) {
	colonIdx := strings.LastIndexByte(ref, ':')
	if colonIdx <= 0 || colonIdx == len(ref)-1 {
		return "", "", 0, false
	}
	filePath = ref[:colonIdx]
	linePart = ref[colonIdx+1:]

	// filePath must look like a path (contain '/' or a file extension)
	base := filepath.Base(filePath)
	if !strings.Contains(filePath, "/") && !strings.Contains(base, ".") {
		return "", "", 0, false
	}

	startPart := linePart
	if dashIdx := strings.IndexByte(linePart, '-'); dashIdx >= 0 {
		startPart = linePart[:dashIdx]
	}
	line, err := strconv.Atoi(startPart)
	if err != nil || line <= 0 {
		return "", "", 0, false
	}
	return filePath, linePart, line, true
}
