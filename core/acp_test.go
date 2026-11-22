package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
	"github.com/stretchr/testify/require"
)

type testACPClient struct {
	mu            sync.Mutex
	updates       []acp.SessionNotification
	updateC       chan acp.SessionNotification
	elicitHandler func(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
}

func newTestACPClient() *testACPClient {
	return &testACPClient{
		updateC: make(chan acp.SessionNotification, 128),
	}
}

func (c *testACPClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.updates = append(c.updates, params)
	c.mu.Unlock()
	select {
	case c.updateC <- params:
	default:
	}
	return nil
}

func (c *testACPClient) ReadTextFile(ctx context.Context, _ acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, ctx.Err()
}

func (c *testACPClient) WriteTextFile(ctx context.Context, _ acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, ctx.Err()
}

func (c *testACPClient) RequestPermission(ctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, ctx.Err()
}

func (c *testACPClient) CreateTerminal(ctx context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, ctx.Err()
}

func (c *testACPClient) KillTerminal(ctx context.Context, _ acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, ctx.Err()
}

func (c *testACPClient) TerminalOutput(ctx context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, ctx.Err()
}

func (c *testACPClient) ReleaseTerminal(ctx context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, ctx.Err()
}

func (c *testACPClient) WaitForTerminalExit(ctx context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, ctx.Err()
}

func (c *testACPClient) UnstableCreateElicitation(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	c.mu.Lock()
	handler := c.elicitHandler
	c.mu.Unlock()
	if handler != nil {
		return handler(ctx, params)
	}
	return acp.UnstableCreateElicitationResponse{}, ctx.Err()
}

func (c *testACPClient) snapshotUpdates() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]acp.SessionNotification, len(c.updates))
	copy(out, c.updates)
	return out
}

func setupACPPair(t *testing.T, ctx context.Context, cancelSession context.CancelFunc, opts ...ACPOption) (Reporter, *acp.ClientSideConnection, *testACPClient, *bytes.Buffer, func()) {
	t.Helper()

	// agentToClient: agent writes to agentOutW, client reads from agentOutR
	agentOutR, agentOutW := io.Pipe()
	// clientToAgent: client writes to clientOutW, agent reads from clientOutR
	clientOutR, clientOutW := io.Pipe()

	var stderrBuf bytes.Buffer
	reporter := NewACPReporter(ctx, agentOutW, clientOutR, &stderrBuf, cancelSession, opts...)

	client := newTestACPClient()
	clientConn := acp.NewClientSideConnection(client, clientOutW, agentOutR)
	clientConn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

	cleanup := func() {
		_ = clientOutW.Close()
		_ = agentOutW.Close()
		if closer, ok := reporter.(io.Closer); ok {
			_ = closer.Close()
		}
		<-clientConn.Done()
	}

	return reporter, clientConn, client, &stderrBuf, cleanup
}

func writeACPTestPipeline(t *testing.T, dir string) string {
	t.Helper()
	promptFile := filepath.Join(dir, "summary.md")
	require.NoError(t, os.WriteFile(promptFile, []byte("Summarize changes."), 0o644))
	pipelineFile := filepath.Join(dir, "pipeline.toml")
	contents := fmt.Sprintf("[[subagents]]\ntitle = \"PR summary\"\ndescription = \"Summarize changes\"\nprompt_file = %q\nrequired = true\nuser_visible = true\nreviewer_input = true\n", promptFile)
	require.NoError(t, os.WriteFile(pipelineFile, []byte(contents), 0o644))
	return pipelineFile
}

func waitForActivePromptTurn(t *testing.T, reporter Reporter) {
	t.Helper()
	acpRep, ok := reporter.(*acpReporter)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		acpRep.mu.Lock()
		defer acpRep.mu.Unlock()
		return acpRep.activeCancel != nil
	}, 2*time.Second, 5*time.Millisecond)
}

func TestACPReporter_FullLifecycleAndPostReviewChat(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, client, stderrBuf, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	// 1. Handshake: Initialize + NewSession
	initResp, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)
	require.Equal(t, acp.ProtocolVersion(acp.ProtocolVersionNumber), initResp.ProtocolVersion)
	require.NotNil(t, initResp.AgentInfo)
	require.Equal(t, "cassandra", initResp.AgentInfo.Name)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)
	_, parseErr := uuid.Parse(string(newSessResp.SessionId))
	require.NoError(t, parseErr)
	require.NotNil(t, newSessResp.Modes)
	require.Equal(t, acpModeReview, newSessResp.Modes.CurrentModeId)
	require.Len(t, newSessResp.Modes.AvailableModes, 1)
	require.Equal(t, "Code review", newSessResp.Modes.AvailableModes[0].Name)

	// 2. Simulate initial review streaming through core.Reporter methods
	cfg := config.NewDefaultConfig()
	cfg.Provider = "google"
	cfg.Model = "gemini-test"
	cfg.Render = "acp"
	cfg.InteractivePostReview = true
	cfg.Base = "main"
	cfg.Head = "HEAD"

	reporter.ReportConfig(cfg, "/workspace")
	reporter.ReportFetchingDiff()
	reporter.ReportFetchingCommits()
	reporter.ReportMCPStatus("github", "connected", nil)
	reporter.ReportMCPStatus("broken", "failed", errors.New("connection refused"))
	reporter.ReportIteration(1)

	testCalls := []llm.ToolCall{
		{
			ID:        "tc_state",
			Name:      "emit_reviewer_state",
			Arguments: `{"focus_area":"security","message":"Checking auth middleware"}`,
		},
		{
			ID:        "tc_read_1",
			Name:      "read_file",
			Arguments: `{"file_path":"auth.go","start_line":1,"end_line":20}`,
		},
		{
			ID:        "",
			Name:      "grep_files",
			Arguments: `{"pattern":"TODO"}`,
		},
	}
	reporter.ReportToolCalls(testCalls)
	reporter.ReportToolStatus(testCalls[1], "started", nil)
	reporter.ReportToolStatus(testCalls[1], "completed", nil)
	reporter.ReportToolStatus(testCalls[2], "failed", errors.New("grep error"))

	require.NoError(t, reporter.ReportStageReview("Pre-Review Summary", "Summary text"))
	reporter.ReportFinalReview()
	require.NoError(t, reporter.ReportReview("Initial automated review findings."))
	reporter.ReportReviewHeader(1, "general", "gemini-test")
	reporter.ReportWarning("minor warning", errors.New("disk slow"))
	reporter.ReportError(errors.New("non-fatal error"))

	// 3. Start Interactive Post-Review and send multiple ACP Prompt requests
	lm := &mockLLM{
		responses: []*llm.Response{
			{
				Text: "First ACP chat reply",
				Usage: llm.Usage{
					PromptTokens: 100,
					OutputTokens: 50,
				},
			},
			{
				Text: "Second ACP chat reply",
				Usage: llm.Usage{
					PromptTokens: 120,
					OutputTokens: 60,
				},
			},
		},
	}
	dispatcher := newMockDispatcher()
	reviewer := &Reviewer{
		Agent:  NewAgent(lm, dispatcher, WithReporter(reporter)),
		Config: cfg,
	}
	reviewer.Agent.history = []llm.Message{
		{Role: llm.RoleSystem, Text: "stable system"},
		{Role: llm.RoleUser, Text: "diff"},
		{Role: llm.RoleAssistant, Text: "Initial automated review findings."},
	}

	postReviewDone := make(chan error, 1)
	go func() {
		postReviewDone <- reviewer.RunInteractivePostReview(sessionCtx)
	}()

	promptResp1, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Why did you flag auth.go?"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp1.StopReason)

	promptResp2, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("What about this resource?"),
			acp.ResourceLinkBlock("auth.go", "file:///workspace/auth.go"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp2.StopReason)

	// Close session via ACP CloseSession RPC using a distinct request context
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	_, err = clientConn.CloseSession(closeCtx, acp.CloseSessionRequest{
		SessionId: newSessResp.SessionId,
	})
	require.NoError(t, err)

	require.NoError(t, <-postReviewDone)

	// Verify discussion metrics were accumulated across both ACP turns
	require.NotNil(t, reviewer.DiscussionMetrics)
	require.Equal(t, 220, reviewer.DiscussionMetrics.Tokens.Input)
	require.Equal(t, 110, reviewer.DiscussionMetrics.Tokens.Output)

	// Verify updates received by the ACP client
	updates := client.snapshotUpdates()
	require.NotEmpty(t, updates)

	var (
		thoughts      []string
		messages      []string
		toolStarts    []*acp.SessionUpdateToolCall
		toolCompleted int
		toolFailed    int
		sessionInfos  []*acp.SessionSessionInfoUpdate
		configUpdates []*acp.SessionConfigOptionUpdate
		plans         []*acp.SessionUpdatePlan
		usageUpdates  []*acp.SessionUsageUpdate
	)
	for _, n := range updates {
		require.Equal(t, newSessResp.SessionId, n.SessionId)
		u := n.Update
		if u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil {
			thoughts = append(thoughts, u.AgentThoughtChunk.Content.Text.Text)
		}
		if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
			messages = append(messages, u.AgentMessageChunk.Content.Text.Text)
		}
		if u.ToolCall != nil {
			toolStarts = append(toolStarts, u.ToolCall)
		}
		if u.ToolCallUpdate != nil && u.ToolCallUpdate.Status != nil {
			switch *u.ToolCallUpdate.Status {
			case acp.ToolCallStatusCompleted:
				toolCompleted++
			case acp.ToolCallStatusFailed:
				toolFailed++
			}
		}
		if u.SessionInfoUpdate != nil {
			sessionInfos = append(sessionInfos, u.SessionInfoUpdate)
		}
		if u.ConfigOptionUpdate != nil {
			configUpdates = append(configUpdates, u.ConfigOptionUpdate)
		}
		if u.Plan != nil {
			plans = append(plans, u.Plan)
		}
		if u.UsageUpdate != nil {
			usageUpdates = append(usageUpdates, u.UsageUpdate)
		}
	}

	require.Contains(t, thoughts, "[security] Checking auth middleware\n")
	require.Contains(t, messages, "# Pre-Review Summary\n\nSummary text\n\n")
	require.Contains(t, messages, "Initial automated review findings.\n\n")
	require.Contains(t, messages, "First ACP chat reply\n\n")
	require.Contains(t, messages, "Second ACP chat reply\n\n")
	require.Len(t, toolStarts, 2)
	require.Equal(t, acp.ToolKindRead, toolStarts[0].Kind)
	require.Len(t, toolStarts[0].Locations, 1)
	require.Equal(t, "/workspace/auth.go", toolStarts[0].Locations[0].Path)
	require.NotNil(t, toolStarts[0].Locations[0].Line)
	require.Equal(t, 1, *toolStarts[0].Locations[0].Line)
	require.Equal(t, acp.ToolKindSearch, toolStarts[1].Kind)
	require.Equal(t, 1, toolCompleted)
	require.Equal(t, 1, toolFailed)

	// Verify SessionInfoUpdate (title, metadata)
	require.NotEmpty(t, sessionInfos)
	require.NotNil(t, sessionInfos[0].Title)
	require.Equal(t, "Review HEAD", *sessionInfos[0].Title)
	require.Equal(t, "google", sessionInfos[0].Meta["provider"])

	// Verify main model is reported in ConfigOptionUpdate
	require.NotEmpty(t, configUpdates)
	require.Len(t, configUpdates[0].ConfigOptions, 1)
	require.NotNil(t, configUpdates[0].ConfigOptions[0].Select)
	require.Equal(t, acp.SessionConfigValueId(acpDefaultModelID), configUpdates[0].ConfigOptions[0].Select.CurrentValue)

	// Verify plan updates
	require.NotEmpty(t, plans)
	latestPlan := plans[len(plans)-1]
	require.NotEmpty(t, latestPlan.Entries)
	var planContents []string
	for _, entry := range latestPlan.Entries {
		planContents = append(planContents, entry.Content)
		require.Equal(t, acp.PlanEntryStatusCompleted, entry.Status)
		require.NotContains(t, strings.ToLower(entry.Content), "interactive", "plan should not include interactive discussion")
	}
	require.Contains(t, planContents, planStepContext)
	require.Contains(t, planContents, planStepReview)

	// Verify token usage updates during chat turns
	require.NotEmpty(t, usageUpdates)
	require.Equal(t, 150, usageUpdates[0].Used)

	// Verify diagnostics went to stderr, not stdout
	stderrText := stderrBuf.String()
	require.Contains(t, stderrText, "connection refused")
	require.Contains(t, stderrText, "minor warning")
	require.Contains(t, stderrText, "non-fatal error")
}

type blockingCancelLLM struct {
	started chan struct{}
}

func (m *blockingCancelLLM) GenerateContent(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ int) (*llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingCancelLLM) GenerateStructuredContent(ctx context.Context, _ []llm.Message, _ map[string]any, _ llm.StructuredConfig) (*llm.Response, error) {
	return nil, ctx.Err()
}

func TestACPReporter_CancelInFlightPrompt(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	blockingLLM := &blockingCancelLLM{started: make(chan struct{})}
	cfg := config.NewDefaultConfig()
	cfg.Render = "acp"

	reviewer := &Reviewer{
		Agent:  NewAgent(blockingLLM, newMockDispatcher(), WithReporter(reporter)),
		Config: cfg,
	}

	postReviewDone := make(chan error, 1)
	go func() {
		postReviewDone <- reviewer.RunInteractivePostReview(sessionCtx)
	}()

	promptDone := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
			SessionId: newSessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Long running question")},
		})
		if err != nil {
			promptErr <- err
			return
		}
		promptDone <- resp
	}()

	// Wait until the LLM call has started, then send ACP Cancel notification
	<-blockingLLM.started
	require.NoError(t, clientConn.Cancel(sessionCtx, acp.CancelNotification{
		SessionId: newSessResp.SessionId,
	}))

	select {
	case err := <-promptErr:
		t.Fatalf("unexpected error from Prompt: %v", err)
	case resp := <-promptDone:
		require.Equal(t, acp.StopReasonCancelled, resp.StopReason)
	}

	cancelSession()
	require.NoError(t, <-postReviewDone)
}

func TestACPReporter_ConcurrentOutOfOrderToolCalls(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	_, err = clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	tc1 := llm.ToolCall{
		ID:        "call_read_a",
		Name:      "read_file",
		Arguments: `{"file_path":"a.go"}`,
	}
	tc2 := llm.ToolCall{
		ID:        "call_read_b",
		Name:      "read_file",
		Arguments: `{"file_path":"b.go"}`,
	}

	reporter.ReportToolCalls([]llm.ToolCall{tc1, tc2})

	// Finish tc2 FIRST with an error, out of order relative to dispatch
	reporter.ReportToolStatus(tc2, "failed", errors.New("file b.go not found"))
	// Finish tc1 SECOND with success
	reporter.ReportToolStatus(tc1, "completed", nil)

	// Verify that client updates correctly correlated by ToolCallId, not FIFO queue
	var updates []*acp.SessionToolCallUpdate
	require.Eventually(t, func() bool {
		updates = nil
		for _, n := range client.snapshotUpdates() {
			if n.Update.ToolCallUpdate != nil {
				updates = append(updates, n.Update.ToolCallUpdate)
			}
		}
		return len(updates) == 2
	}, 2*time.Second, 10*time.Millisecond)

	var starts []*acp.SessionUpdateToolCall
	for _, n := range client.snapshotUpdates() {
		if n.Update.ToolCall != nil {
			starts = append(starts, n.Update.ToolCall)
		}
	}
	require.Len(t, starts, 2)
	require.NotEqual(t, starts[0].ToolCallId, starts[1].ToolCallId)

	// Completion IDs reference generated transport IDs, not provider IDs.
	require.Equal(t, starts[1].ToolCallId, updates[0].ToolCallId)
	require.NotNil(t, updates[0].Status)
	require.Equal(t, acp.ToolCallStatusFailed, *updates[0].Status)
	require.Equal(t, "file b.go not found", updates[0].RawOutput)

	// Second update received by client must be for call_read_a and marked completed
	require.Equal(t, starts[0].ToolCallId, updates[1].ToolCallId)
	require.NotNil(t, updates[1].Status)
	require.Equal(t, acp.ToolCallStatusCompleted, *updates[1].Status)
}

func TestACPReporter_ContextCancellationUnblocksSendUpdate(t *testing.T) {
	runnerCtx, runnerCancel := context.WithCancel(context.Background())
	defer runnerCancel()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, runnerCtx, nil)
	defer cleanup()

	// Perform initialize but deliberately do NOT call session/new
	_, err := clientConn.Initialize(runnerCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	unblocked := make(chan struct{})
	go func() {
		// ReportFetchingDiff calls sendUpdate, which would block on sessionReady
		reporter.ReportFetchingDiff()
		close(unblocked)
	}()

	// Canceling the runner context must unblock sendUpdate immediately
	runnerCancel()

	select {
	case <-unblocked:
		// Success: sendUpdate unblocked
	case <-time.After(2 * time.Second):
		t.Fatal("sendUpdate remained blocked after runner context cancellation")
	}
}

func TestACPReporter_CloseSessionManagedCancellation(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	canceledCount := 0
	var mu sync.Mutex
	safeCancel := func() {
		mu.Lock()
		canceledCount++
		mu.Unlock()
		cancelSession()
	}

	_, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, safeCancel)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	// Call CloseSession twice concurrently
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, closeErr := clientConn.CloseSession(context.Background(), acp.CloseSessionRequest{
				SessionId: newSessResp.SessionId,
			})
			require.NoError(t, closeErr)
		}()
	}
	wg.Wait()

	// Wait briefly for managed shutdown goroutine to complete
	select {
	case <-sessionCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("sessionCtx was not canceled by CloseSession")
	}

	mu.Lock()
	require.Equal(t, 1, canceledCount, "cancelSession should be invoked exactly once via sync.Once")
	mu.Unlock()
}

func TestACPReporter_PromptCancellationDuringInitialReview(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	// Start Prompt turn while initial review has NOT finished (reviewReady is not closed)
	promptDone := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
			SessionId: newSessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Question before review finishes")},
		})
		if err != nil {
			promptErr <- err
			return
		}
		promptDone <- resp
	}()

	waitForActivePromptTurn(t, reporter)

	// Cancel via ACP session/cancel
	require.NoError(t, clientConn.Cancel(sessionCtx, acp.CancelNotification{
		SessionId: newSessResp.SessionId,
	}))

	select {
	case err := <-promptErr:
		t.Fatalf("unexpected error from Prompt: %v", err)
	case resp := <-promptDone:
		require.Equal(t, acp.StopReasonCancelled, resp.StopReason)
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not cancel during initial review wait")
	}
}

func TestACPReporter_RejectConcurrentPromptTurns(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	// First Prompt will block on reviewReady
	turn1Done := make(chan struct{}, 1)
	go func() {
		defer close(turn1Done)
		_, _ = clientConn.Prompt(sessionCtx, acp.PromptRequest{
			SessionId: newSessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Turn 1")},
		})
	}()

	waitForActivePromptTurn(t, reporter)

	// Second Prompt concurrently should be rejected
	_, err = clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("Turn 2")},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "concurrent prompt turns are not allowed")

	cancelSession()
	select {
	case <-turn1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Turn 1 prompt goroutine did not exit after cancellation")
	}
}

func TestACPReporter_StageReportsStreamInCompletionOrder(t *testing.T) {
	for _, waitForMessage := range []bool{false, true} {
		t.Run(fmt.Sprintf("wait-for-message=%t", waitForMessage), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reporter, conn, client, _, cleanup := setupACPPair(t, ctx, cancel)
			defer cleanup()
			_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
			require.NoError(t, err)
			cfg := config.NewDefaultConfig()
			cfg.WaitForMessage = waitForMessage
			cfg.ReviewPipelineFile = "pipeline.toml"
			reporter.ReportConfig(cfg, t.TempDir())

			messages := func() []string {
				var texts []string
				for _, update := range client.snapshotUpdates() {
					if chunk := update.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
						texts = append(texts, chunk.Content.Text.Text)
					}
				}
				return texts
			}
			reporter.ReportStageIteration("Subagent: Security", 1)
			reporter.ReportStageIteration("Subagent: AI Code Review", 1)
			require.NoError(t, reporter.ReportStageReview("AI Code Review", "First completed subagent"))
			require.Eventually(t, func() bool { return len(messages()) == 1 }, time.Second, time.Millisecond)
			require.NoError(t, reporter.ReportStageReview("Security", "Second completed subagent"))
			require.Eventually(t, func() bool { return len(messages()) == 2 }, time.Second, time.Millisecond)
			rep := reporter.(*acpReporter)
			rep.mu.Lock()
			pipelineStatus := rep.planStatuses[planStepPipeline]
			reviewStatus := rep.planStatuses[planStepReview]
			rep.mu.Unlock()
			require.Equal(t, acp.PlanEntryStatusInProgress, pipelineStatus, "one completed report does not finish the entire pipeline")
			require.Equal(t, acp.PlanEntryStatusPending, reviewStatus)

			reporter.ReportStageIteration("AI Code Review", 1)
			require.NoError(t, reporter.ReportStageReview("Empty", ""))
			require.NoError(t, reporter.ReportReview("Main review"))
			require.Eventually(t, func() bool { return len(messages()) == 3 }, time.Second, time.Millisecond)
			require.Equal(t, []string{
				"# AI Code Review\n\nFirst completed subagent\n\n",
				"# Security\n\nSecond completed subagent\n\n",
				"Main review\n\n",
			}, messages())
			var pipelinePlans int
			for _, update := range client.snapshotUpdates() {
				if plan := update.Update.Plan; plan != nil {
					for _, entry := range plan.Entries {
						if entry.Content == planStepPipeline {
							pipelinePlans++
						}
					}
				}
			}
			require.Positive(t, pipelinePlans)
		})
	}
}

func TestACPReporter_WaitForMessage(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	// 1. Handshake: Initialize + NewSession succeed immediately
	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	pipelineFile := writeACPTestPipeline(t, tmpDir)

	cfg := config.NewDefaultConfig()
	cfg.Provider = "google"
	cfg.Model = "gemini-test"
	cfg.Render = "acp"
	cfg.InteractivePostReview = true
	cfg.WaitForMessage = true
	cfg.ReviewPipelineFile = pipelineFile

	reporter.ReportConfig(cfg, tmpDir)
	reporter.ReportFetchingDiff()

	lm := &mockLLM{
		responses: []*llm.Response{
			{
				Text:  "Pre-review summary result",
				Usage: llm.Usage{PromptTokens: 50, OutputTokens: 25},
			},
			{
				Text:  "Initial review result",
				Usage: llm.Usage{PromptTokens: 100, OutputTokens: 50},
			},
			{
				Text:  "Reply to queued first user message",
				Usage: llm.Usage{PromptTokens: 150, OutputTokens: 75},
			},
		},
	}

	reviewer := &Reviewer{
		Agent:        NewAgent(lm, newMockDispatcher(), WithReporter(reporter)),
		Config:       cfg,
		RootDir:      tmpDir,
		StablePrompt: "stable system prompt",
		Guidelines:   "general guidelines",
	}

	reviewer.llmFactory = func(ctx context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
		return lm, ctx.Err()
	}

	// Start Reviewer.Run + RunInteractivePostReview in background
	pipelineDone := make(chan error, 1)
	go func() {
		reviewText, runErr := reviewer.Run(sessionCtx, []string{"file.go"}, "diff content")
		if runErr != nil {
			pipelineDone <- runErr
			return
		}
		if repErr := reporter.ReportReview(reviewText); repErr != nil {
			pipelineDone <- repErr
			return
		}
		pipelineDone <- reviewer.RunInteractivePostReview(sessionCtx)
	}()

	// Verify that after session/new, config updates were sent, but NO LLM calls
	// have started because no user message (session/prompt) has been sent yet.
	require.Eventually(t, func() bool {
		return len(client.snapshotUpdates()) > 0
	}, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, 0, lm.callIdx, "pre-review and initial review must not start before first user message")

	// 2. Send the first user message via session/prompt.
	// This unblocks Reviewer.Run, appends the message inside
	// <extra_initial_user_context_or_guidelines>, runs pre-review + initial
	// review, and completes Turn 1 once the review is emitted.
	promptResp1, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Please focus on concurrency in file.go"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp1.StopReason)

	// Turn 1 must have executed only the pre-review (1) and initial review (2) passes
	require.Equal(t, 2, lm.callIdx)

	// Both passes must have received the <extra_initial_user_context_or_guidelines> block
	expectedExtraBlock := "<extra_initial_user_context_or_guidelines>\nPlease focus on concurrency in file.go\n</extra_initial_user_context_or_guidelines>"
	require.Contains(t, lm.calls[0][len(lm.calls[0])-1].Text, expectedExtraBlock)
	require.Contains(t, lm.calls[1][len(lm.calls[1])-1].Text, expectedExtraBlock)

	// 3. Send a second user message (follow-up question) and verify it runs as a post-review chat turn
	promptResp2, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Follow-up question about finding 1"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp2.StopReason)
	require.Equal(t, 3, lm.callIdx)

	var messages []string
	messageIDs := make(map[string]bool)
	for _, n := range client.snapshotUpdates() {
		if n.Update.AgentMessageChunk != nil && n.Update.AgentMessageChunk.Content.Text != nil {
			messages = append(messages, n.Update.AgentMessageChunk.Content.Text.Text)
			require.NotNil(t, n.Update.AgentMessageChunk.MessageId)
			messageIDs[*n.Update.AgentMessageChunk.MessageId] = true
		}
	}
	require.Len(t, messages, 3)
	require.Len(t, messageIDs, 3, "stage report, review, and follow-up must have distinct MessageIds")
	require.Equal(t, "# PR summary\n\nPre-review summary result\n\n", messages[0])
	require.Equal(t, "Initial review result\n\n", messages[1])
	require.Equal(t, "Reply to queued first user message\n\n", messages[2])

	cancelSession()
	require.NoError(t, <-pipelineDone)
}

func TestACPReporter_WaitForMessage_WhitespaceOnlyOmitsExtraBlock(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	cfg := config.NewDefaultConfig()
	cfg.Provider = "google"
	cfg.Model = "gemini-test"
	cfg.Render = "acp"
	cfg.InteractivePostReview = true
	cfg.WaitForMessage = true

	lm := &mockLLM{
		responses: []*llm.Response{
			{Text: "Initial review result"},
		},
	}

	reviewer := &Reviewer{
		Agent:        NewAgent(lm, newMockDispatcher(), WithReporter(reporter)),
		Config:       cfg,
		RootDir:      t.TempDir(),
		StablePrompt: "stable system prompt",
		Guidelines:   "general guidelines",
	}

	pipelineDone := make(chan error, 1)
	go func() {
		reviewText, runErr := reviewer.Run(sessionCtx, []string{"file.go"}, "diff content")
		if runErr != nil {
			pipelineDone <- runErr
			return
		}
		if repErr := reporter.ReportReview(reviewText); repErr != nil {
			pipelineDone <- repErr
			return
		}
		pipelineDone <- reviewer.RunInteractivePostReview(sessionCtx)
	}()

	promptResp, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("   \n\t  "),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp.StopReason)

	require.Equal(t, 1, lm.callIdx)
	userMsg := lm.calls[0][len(lm.calls[0])-1].Text
	require.Equal(t, "diff content", userMsg)
	require.NotContains(t, userMsg, "extra_initial_user_context_or_guidelines")

	cancelSession()
	require.NoError(t, <-pipelineDone)
}

func TestACPReporter_PipelineDoesNotWaitForFocusMessage(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	pipelineFile := writeACPTestPipeline(t, tmpDir)

	cfg := config.NewDefaultConfig()
	cfg.Provider = "google"
	cfg.Model = "gemini-test"
	cfg.Render = "acp"
	cfg.InteractivePostReview = true
	cfg.WaitForMessage = false
	cfg.ReviewPipelineFile = pipelineFile

	reporter.ReportConfig(cfg, tmpDir)

	lm := &mockLLM{
		responses: []*llm.Response{
			{Text: "Pre-review summary output"},
			{Text: "Main review output"},
			{Text: "Post-review chat answer"},
			{Text: "Second post-review answer"},
		},
	}

	reviewer := &Reviewer{
		Agent:        NewAgent(lm, newMockDispatcher(), WithReporter(reporter)),
		Config:       cfg,
		RootDir:      tmpDir,
		StablePrompt: "stable system prompt",
		Guidelines:   "general guidelines",
		llmFactory: func(ctx context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
			return lm, ctx.Err()
		},
	}

	pipelineDone := make(chan error, 1)
	go func() {
		reviewText, runErr := reviewer.Run(sessionCtx, []string{"auth.go"}, "diff content")
		if runErr != nil {
			pipelineDone <- runErr
			return
		}
		if repErr := reporter.ReportReview(reviewText); repErr != nil {
			pipelineDone <- repErr
			return
		}
		pipelineDone <- reviewer.RunInteractivePostReview(sessionCtx)
	}()

	// Pipeline and main review complete without any user prompt.
	acpRep := reporter.(*acpReporter)
	select {
	case <-acpRep.postReviewReady:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline and main review should not wait for a focus message")
	}
	require.Equal(t, 2, lm.callIdx)
	mainReviewUserMsg := lm.calls[1][len(lm.calls[1])-1].Text
	require.Contains(t, mainReviewUserMsg, "PR summary")
	require.Contains(t, mainReviewUserMsg, "Pre-review summary output")
	require.NotContains(t, mainReviewUserMsg, "extra_initial_user_context_or_guidelines")

	// The first message is a post-review question, not an implicit focus gate.
	promptResp1, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Focus on error handling in auth.go"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp1.StopReason)
	require.Equal(t, 3, lm.callIdx)

	// 3. Subsequent prompt runs as a post-review chat turn
	promptResp2, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Why did you flag line 10?"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp2.StopReason)
	require.Equal(t, 4, lm.callIdx)
	for _, update := range client.snapshotUpdates() {
		if chunk := update.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			require.NotContains(t, chunk.Content.Text.Text, "Is there anything you want me to focus on")
		}
	}

	cancelSession()
	require.NoError(t, <-pipelineDone)
}

func TestLinkifyFileReferences(t *testing.T) {
	input := strings.Join([]string{
		"- [blocking] [core/acp.go:42] race condition",
		"- [nit] [core/reviewer.go:10-20] simplify loop",
		"- `[issue] [cmd/ai_reviewer/main.go:15-30]` check flag",
		"- `[auth.go:99]` standalone file",
		"- check `tools/local_tools.go:50-60` in prose",
		"- already linked [core/acp.go:42](/custom/link#L42)",
		"```go",
		"// [core/acp.go:42] inside code block must not change",
		"```",
	}, "\n")

	got := linkifyFileReferences(input, "/workspace")

	require.Contains(t, got, "- [blocking] [core/acp.go:42](/workspace/core/acp.go#L42) race condition")
	require.Contains(t, got, "- [nit] [core/reviewer.go:10-20](/workspace/core/reviewer.go#L10) simplify loop")
	require.Contains(t, got, "- `[issue]` [cmd/ai_reviewer/main.go:15-30](/workspace/cmd/ai_reviewer/main.go#L15) check flag")
	require.Contains(t, got, "- [auth.go:99](/workspace/auth.go#L99) standalone file")
	require.Contains(t, got, "- check [tools/local_tools.go:50-60](/workspace/tools/local_tools.go#L50) in prose")
	require.Contains(t, got, "- already linked [core/acp.go:42](/custom/link#L42)")
	require.Contains(t, got, "// [core/acp.go:42] inside code block must not change")
}

func TestACPReporter_WaitForMessage_NoChangesDoesNotDropFirstMessage(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newSessResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	tmpDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	cfg.Provider = "google"
	cfg.Model = "gemini-test"
	cfg.Render = "acp"
	cfg.InteractivePostReview = true
	cfg.WaitForMessage = true

	reporter.ReportConfig(cfg, tmpDir)

	lm := &mockLLM{
		responses: []*llm.Response{
			{Text: "There are no modified files in this commit."},
		},
	}
	reviewer := &Reviewer{
		Agent:   NewAgent(lm, newMockDispatcher(), WithReporter(reporter)),
		Config:  cfg,
		RootDir: tmpDir,
	}

	// In main.go when len(changedFiles) == 0, ReportNoChanges is emitted and then
	// RunInteractivePostReview is started because cfg.InteractivePostReview is true.
	reporter.ReportNoChanges()

	postReviewDone := make(chan error, 1)
	go func() {
		postReviewDone <- reviewer.RunInteractivePostReview(sessionCtx)
	}()

	// The user's very first message must be handled by the post-review handler,
	// rather than dropped by a false positive on waitForMessage.
	promptResp, err := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Why were no changes detected?"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, promptResp.StopReason)
	require.Equal(t, 1, lm.callIdx, "post-review LLM must be called for the user's first prompt")

	cancelSession()
	require.NoError(t, <-postReviewDone)
}

func TestResolveSessionDetails_WithPRMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	metadataPath := filepath.Join(tmpDir, "pr_metadata.json")
	pr := PRMetadata{
		Title:    "Fix authentication timeout",
		PRNumber: 132,
		Author:   "developer_one",
	}
	data, err := json.Marshal(pr)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metadataPath, data, 0o644))

	cfg := &config.Config{
		MetadataJSONFile: metadataPath,
	}

	title, desc := resolveSessionDetails(context.Background(), tmpDir, cfg)
	require.Equal(t, "Review Fix authentication timeout", title)
	require.Equal(t, "Reviewing PR 132 by developer_one", desc)
}

func TestResolveSessionDetails_WithoutPRMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Head: "feat/my-branch",
	}

	title, desc := resolveSessionDetails(context.Background(), tmpDir, cfg)
	require.Equal(t, "Review feat/my-branch", title)
	require.Empty(t, desc)
}

func TestResolveSessionDetails_WithPRMetadata_ZeroPRNumber(t *testing.T) {
	tmpDir := t.TempDir()
	metadataPath := filepath.Join(tmpDir, "pr_metadata.json")
	pr := PRMetadata{
		Title:    "Local changes",
		PRNumber: 0,
		Author:   "developer_one",
	}
	data, err := json.Marshal(pr)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metadataPath, data, 0o644))

	cfg := &config.Config{
		MetadataJSONFile: metadataPath,
	}

	title, desc := resolveSessionDetails(context.Background(), tmpDir, cfg)
	require.Equal(t, "Review Local changes", title)
	require.Equal(t, "Reviewing changes by developer_one", desc)
}

func TestResolveSessionDetails_WithPRMetadata_ZeroPRNumber_NoAuthor(t *testing.T) {
	tmpDir := t.TempDir()
	metadataPath := filepath.Join(tmpDir, "pr_metadata.json")
	pr := PRMetadata{
		Title:    "Local changes without author",
		PRNumber: 0,
	}
	data, err := json.Marshal(pr)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metadataPath, data, 0o644))

	cfg := &config.Config{
		MetadataJSONFile: metadataPath,
	}

	title, desc := resolveSessionDetails(context.Background(), tmpDir, cfg)
	require.Equal(t, "Review Local changes without author", title)
	require.Empty(t, desc)
}

func TestResolveSessionDetails_EmptyTargetDirWithGit(t *testing.T) {
	tmpDir := t.TempDir()
	runGitCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}
	runGitCmd("init", "-b", "feature-test")
	runGitCmd("config", "user.email", "test@example.com")
	runGitCmd("config", "user.name", "Test User")
	runGitCmd("config", "commit.gpgsign", "false")

	testFile := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("content"), 0o644))
	runGitCmd("add", "test.txt")
	runGitCmd("commit", "-m", "Initial test commit")

	t.Chdir(tmpDir)

	title, desc := resolveSessionDetails(context.Background(), "", nil)
	require.Equal(t, "Review feature-test", title)
	require.Contains(t, desc, "Initial test commit")
}

func TestACPReporter_Initialize_AdvertisesLoadSession(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	_, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	initResp, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)
	require.True(t, initResp.AgentCapabilities.LoadSession)
}

func TestACPReporter_LoadSession_Success(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	tmpDir := t.TempDir()
	store := NewSessionStore(tmpDir)

	sessID := "sess_resume_123"
	initialMsgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "stable system prompt"},
		{Role: llm.RoleUser, Text: "Review this diff"},
		{
			Role:      llm.RoleAssistant,
			Reasoning: "Examining auth logic",
			ToolCalls: []llm.ToolCall{
				{
					ID:        "call_1",
					Name:      "read_file",
					Arguments: `{"file_path":"auth.go"}`,
				},
			},
		},
		{
			Role: llm.RoleTool,
			ToolResults: []llm.ToolResult{
				{
					ToolCallID: "call_1",
					Name:       "read_file",
					Content:    "package auth",
				},
			},
		},
		{Role: llm.RoleAssistant, Text: "Review findings: LGTM"},
	}
	require.NoError(t, store.AppendMessages(sessionCtx, sessID, initialMsgs...))

	reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession, WithACPSessionStore(store))
	defer cleanup()

	initResp, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)
	require.True(t, initResp.AgentCapabilities.LoadSession)

	loadResp, err := clientConn.LoadSession(sessionCtx, acp.LoadSessionRequest{
		SessionId:  acp.SessionId(sessID),
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)
	require.NotNil(t, loadResp.Modes)
	require.Equal(t, acpModeReview, loadResp.Modes.CurrentModeId)

	sessWaiter, ok := reporter.(SessionWaiter)
	require.True(t, ok)
	require.NoError(t, sessWaiter.WaitForSession(sessionCtx))
	require.True(t, sessWaiter.IsLoadedSession())
	require.Equal(t, sessID, sessWaiter.SessionID())
	require.Len(t, sessWaiter.LoadedMessages(), len(initialMsgs))

	// Verify replayed updates received by the client
	require.Eventually(t, func() bool {
		var hasUserMsg, hasThought, hasToolCall, hasAgentMsg bool
		for _, n := range client.snapshotUpdates() {
			if n.Update.UserMessageChunk != nil && n.Update.UserMessageChunk.Content.Text != nil {
				if n.Update.UserMessageChunk.Content.Text.Text == "Review this diff" {
					hasUserMsg = true
				}
			}
			if n.Update.AgentThoughtChunk != nil && n.Update.AgentThoughtChunk.Content.Text != nil {
				if strings.Contains(n.Update.AgentThoughtChunk.Content.Text.Text, "Examining auth logic") {
					hasThought = true
				}
			}
			if n.Update.ToolCall != nil {
				if n.Update.ToolCall.RawOutput == "package auth" && n.Update.ToolCall.ToolCallId != "" {
					hasToolCall = true
				}
			}
			if n.Update.AgentMessageChunk != nil && n.Update.AgentMessageChunk.Content.Text != nil {
				if strings.Contains(n.Update.AgentMessageChunk.Content.Text.Text, "Review findings: LGTM") {
					hasAgentMsg = true
				}
			}
		}
		return hasUserMsg && hasThought && hasToolCall && hasAgentMsg
	}, 2*time.Second, 5*time.Millisecond)

	// Verify interactive post-review receives prompt turns even if Prompt arrives
	// BEFORE ServePostReview registers the handler (reproducing fast client prompt arrival)
	server, ok := reporter.(PostReviewServer)
	require.True(t, ok)
	chatTurnHandled := make(chan string, 1)

	promptDone := make(chan struct{})
	var promptResp acp.PromptResponse
	var promptErr error
	go func() {
		defer close(promptDone)
		promptResp, promptErr = clientConn.Prompt(sessionCtx, acp.PromptRequest{
			SessionId: acp.SessionId(sessID),
			Prompt: []acp.ContentBlock{
				acp.TextBlock("What about performance?"),
			},
		})
	}()

	// Delay entering ServePostReview to verify Prompt waits cleanly on reviewReady
	time.Sleep(25 * time.Millisecond)
	go func() {
		_ = server.ServePostReview(sessionCtx, func(_ context.Context, query string) error {
			chatTurnHandled <- query
			return nil
		})
	}()

	<-promptDone
	require.NoError(t, promptErr)
	require.Equal(t, acp.StopReasonEndTurn, promptResp.StopReason)
	require.Equal(t, "What about performance?", <-chatTurnHandled)
}

func TestACPReporter_SignalReviewFailed(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	server, ok := reporter.(PostReviewServer)
	require.True(t, ok)

	promptDone := make(chan struct{})
	var promptResp acp.PromptResponse
	var promptErr error
	go func() {
		defer close(promptDone)
		promptResp, promptErr = clientConn.Prompt(sessionCtx, acp.PromptRequest{
			SessionId: newResp.SessionId,
			Prompt: []acp.ContentBlock{
				acp.TextBlock("start review"),
			},
		})
	}()

	time.Sleep(25 * time.Millisecond)
	testErr := errors.New("simulated context overflow")
	require.NoError(t, server.SignalReviewFailed(testErr))

	select {
	case <-promptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Prompt to complete after SignalReviewFailed")
	}

	require.NoError(t, promptErr)
	require.Equal(t, acp.StopReasonEndTurn, promptResp.StopReason)

	// Verify follow-up interactive post-review works after review failure (reviewErr does not stick)
	chatHandled := make(chan string, 1)
	go func() {
		_ = server.ServePostReview(sessionCtx, func(_ context.Context, query string) error {
			chatHandled <- query
			return nil
		})
	}()

	followUpResp, followUpErr := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Why did it fail?"),
		},
	})
	require.NoError(t, followUpErr)
	require.Equal(t, acp.StopReasonEndTurn, followUpResp.StopReason)
	select {
	case query := <-chatHandled:
		require.Equal(t, "Why did it fail?", query)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for follow-up Prompt to be handled in post-review")
	}
}

func TestACPReporter_SignalReviewFailed_PostReviewChat(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
	defer cleanup()

	_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)

	newResp, err := clientConn.NewSession(sessionCtx, acp.NewSessionRequest{
		Cwd:        "/workspace",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)

	server, ok := reporter.(PostReviewServer)
	require.True(t, ok)

	testErr := errors.New("simulated context overflow")
	require.NoError(t, server.SignalReviewFailed(testErr))

	chatHandled := make(chan string, 1)
	go func() {
		_ = server.ServePostReview(sessionCtx, func(_ context.Context, query string) error {
			chatHandled <- query
			return nil
		})
	}()

	promptResp, promptErr := clientConn.Prompt(sessionCtx, acp.PromptRequest{
		SessionId: newResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("Can we try with fewer files?"),
		},
	})
	require.NoError(t, promptErr)
	require.Equal(t, acp.StopReasonEndTurn, promptResp.StopReason)
	select {
	case query := <-chatHandled:
		require.Equal(t, "Can we try with fewer files?", query)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Prompt to be handled in post-review after SignalReviewFailed")
	}
}

func TestACPReporter_LoadSession_Errors(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	t.Run("no session store configured", func(t *testing.T) {
		_, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
		defer cleanup()

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
		})
		require.NoError(t, err)

		_, err = clientConn.LoadSession(sessionCtx, acp.LoadSessionRequest{
			SessionId:  "sess_any",
			Cwd:        "/workspace",
			McpServers: []acp.McpServer{},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "session store not configured")
	})

	t.Run("empty session ID", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := NewSessionStore(tmpDir)

		_, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession, WithACPSessionStore(store))
		defer cleanup()

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
		})
		require.NoError(t, err)

		_, err = clientConn.LoadSession(sessionCtx, acp.LoadSessionRequest{
			SessionId:  "",
			Cwd:        "/workspace",
			McpServers: []acp.McpServer{},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "sessionId is required")
	})

	t.Run("session not found initializes fresh session", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := NewSessionStore(tmpDir)

		reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession, WithACPSessionStore(store))
		defer cleanup()

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
		})
		require.NoError(t, err)

		loadResp, err := clientConn.LoadSession(sessionCtx, acp.LoadSessionRequest{
			SessionId:  "non_existent_session",
			Cwd:        "/workspace",
			McpServers: []acp.McpServer{},
		})
		require.NoError(t, err)
		require.NotNil(t, loadResp.Modes)

		sessWaiter, ok := reporter.(SessionWaiter)
		require.True(t, ok)
		require.NoError(t, sessWaiter.WaitForSession(sessionCtx))
		require.False(t, sessWaiter.IsLoadedSession())
		require.Equal(t, "non_existent_session", sessWaiter.SessionID())
	})

	t.Run("invalid session ID returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := NewSessionStore(tmpDir)

		_, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession, WithACPSessionStore(store))
		defer cleanup()

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
		})
		require.NoError(t, err)

		_, err = clientConn.LoadSession(sessionCtx, acp.LoadSessionRequest{
			SessionId:  "../invalid/session",
			Cwd:        "/workspace",
			McpServers: []acp.McpServer{},
		})
		require.Error(t, err)
	})
}

func TestACP_AskDeveloper_Elicitation(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	t.Run("skips when client does not support elicitation form", func(t *testing.T) {
		reporter, clientConn, _, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
		defer cleanup()

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
		})
		require.NoError(t, err)

		prompter, ok := reporter.(tools.DeveloperPrompter)
		require.True(t, ok)

		resp, err := prompter.AskDeveloper(sessionCtx, tools.AskDeveloperRequest{
			Question:  "Should we use PostgreSQL?",
			Reasoning: "Affects persistence layer.",
		})
		require.NoError(t, err)
		require.Equal(t, "skipped", resp.Status)
		require.Contains(t, resp.Message, "does not support elicitation forms")
	})

	t.Run("answered with single-select options", func(t *testing.T) {
		reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
		defer cleanup()

		client.elicitHandler = func(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
			require.NotNil(t, params.Form)
			require.Contains(t, params.Form.Message, "Which database should we use?")
			require.Contains(t, params.Form.Message, "Reasoning: Need ACID transactions.")
			require.Equal(t, "form", params.Form.Mode)

			return acp.UnstableCreateElicitationResponse{
				Accept: &acp.UnstableCreateElicitationAccept{
					Action: "accept",
					Content: map[string]any{
						"selected": "PostgreSQL",
						"response": "Ensure migrations are applied.",
					},
				},
			}, nil
		}

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
			ClientCapabilities: acp.ClientCapabilities{
				Elicitation: &acp.ElicitationCapabilities{
					Form: &acp.ElicitationFormCapabilities{},
				},
			},
		})
		require.NoError(t, err)

		prompter, ok := reporter.(tools.DeveloperPrompter)
		require.True(t, ok)

		resp, err := prompter.AskDeveloper(sessionCtx, tools.AskDeveloperRequest{
			Question:      "Which database should we use?",
			Reasoning:     "Need ACID transactions.",
			Options:       []string{"PostgreSQL", "SQLite"},
			IsMultiSelect: false,
		})
		require.NoError(t, err)
		require.Equal(t, "answered", resp.Status)
		require.Equal(t, []string{"PostgreSQL"}, resp.Selected)
		require.Equal(t, "Ensure migrations are applied.", resp.Response)
	})

	t.Run("answered with multi-select options", func(t *testing.T) {
		reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
		defer cleanup()

		client.elicitHandler = func(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
			return acp.UnstableCreateElicitationResponse{
				Accept: &acp.UnstableCreateElicitationAccept{
					Action: "accept",
					Content: map[string]any{
						"selected": []any{"frontend", "backend"},
						"response": "Full stack changes.",
					},
				},
			}, nil
		}

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
			ClientCapabilities: acp.ClientCapabilities{
				Elicitation: &acp.ElicitationCapabilities{
					Form: &acp.ElicitationFormCapabilities{},
				},
			},
		})
		require.NoError(t, err)

		prompter, ok := reporter.(tools.DeveloperPrompter)
		require.True(t, ok)

		resp, err := prompter.AskDeveloper(sessionCtx, tools.AskDeveloperRequest{
			Question:      "Which scopes apply?",
			Reasoning:     "Tagging PR.",
			Options:       []string{"frontend", "backend", "infra"},
			IsMultiSelect: true,
		})
		require.NoError(t, err)
		require.Equal(t, "answered", resp.Status)
		require.Equal(t, []string{"frontend", "backend"}, resp.Selected)
		require.Equal(t, "Full stack changes.", resp.Response)
	})

	t.Run("declined by developer", func(t *testing.T) {
		reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
		defer cleanup()

		client.elicitHandler = func(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
			return acp.UnstableCreateElicitationResponse{
				Decline: &acp.UnstableCreateElicitationDecline{
					Action: "decline",
				},
			}, nil
		}

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
			ClientCapabilities: acp.ClientCapabilities{
				Elicitation: &acp.ElicitationCapabilities{
					Form: &acp.ElicitationFormCapabilities{},
				},
			},
		})
		require.NoError(t, err)

		prompter, ok := reporter.(tools.DeveloperPrompter)
		require.True(t, ok)

		resp, err := prompter.AskDeveloper(sessionCtx, tools.AskDeveloperRequest{
			Question:  "Should we refactor this?",
			Reasoning: "Code smell observed.",
		})
		require.NoError(t, err)
		require.Equal(t, "skipped", resp.Status)
		require.Contains(t, resp.Message, "declined to respond")
	})

	t.Run("times out when developer does not answer", func(t *testing.T) {
		reporter, clientConn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
		defer cleanup()

		client.elicitHandler = func(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
			<-ctx.Done()
			return acp.UnstableCreateElicitationResponse{}, ctx.Err()
		}

		_, err := clientConn.Initialize(sessionCtx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
			ClientCapabilities: acp.ClientCapabilities{
				Elicitation: &acp.ElicitationCapabilities{
					Form: &acp.ElicitationFormCapabilities{},
				},
			},
		})
		require.NoError(t, err)

		prompter, ok := reporter.(tools.DeveloperPrompter)
		require.True(t, ok)

		ctxWithTimeout := tools.WithAskDeveloperTimeout(sessionCtx, 50*time.Millisecond)
		resp, err := prompter.AskDeveloper(ctxWithTimeout, tools.AskDeveloperRequest{
			Question:  "Should we refactor this?",
			Reasoning: "Code smell observed.",
		})
		require.NoError(t, err)
		require.Equal(t, "timeout", resp.Status)
		require.Contains(t, resp.Message, "did not respond within")
	})
}

func TestACPReporter_StrictToolIdentitiesAndReusedProviderIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter, conn, client, _, cleanup := setupACPPair(t, ctx, cancel)
	defer cleanup()
	_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/workspace", McpServers: []acp.McpServer{}})
	require.NoError(t, err)

	identified := llm.ToolCall{ID: "provider_id", Name: "read_file", Arguments: `{}`}
	anonymous := llm.ToolCall{Name: "read_file", Arguments: `{}`}
	reporter.ReportToolCalls([]llm.ToolCall{identified, anonymous})
	reporter.ReportToolStatus(llm.ToolCall{ID: "unknown", Name: "read_file"}, "failed", errors.New("must not consume another call"))
	reporter.ReportToolStatus(identified, "unknown status", nil)
	reporter.ReportToolStatus(identified, "started", nil)
	reporter.ReportToolStatus(anonymous, "completed", nil)
	reporter.ReportToolStatus(identified, "failed", errors.New("correct call"))
	reporter.ReportToolStatus(identified, "completed", nil) // Duplicate terminal status.
	reporter.ReportToolCalls([]llm.ToolCall{identified})
	reporter.ReportToolStatus(identified, "completed", nil)

	var starts []*acp.SessionUpdateToolCall
	var ends []*acp.SessionToolCallUpdate
	require.Eventually(t, func() bool {
		starts, ends = nil, nil
		for _, notification := range client.snapshotUpdates() {
			if call := notification.Update.ToolCall; call != nil {
				starts = append(starts, call)
			}
			if update := notification.Update.ToolCallUpdate; update != nil {
				ends = append(ends, update)
			}
		}
		return len(starts) == 3 && len(ends) == 3
	}, 2*time.Second, 5*time.Millisecond)
	for i, call := range starts {
		require.NotEmpty(t, call.ToolCallId)
		require.NotEqual(t, acp.ToolCallId(identified.ID), call.ToolCallId)
		for j := 0; j < i; j++ {
			require.NotEqual(t, starts[j].ToolCallId, call.ToolCallId)
		}
	}
	require.Equal(t, starts[1].ToolCallId, ends[0].ToolCallId)
	require.Equal(t, starts[0].ToolCallId, ends[1].ToolCallId)
	require.Equal(t, "correct call", ends[1].RawOutput)
	require.Equal(t, starts[2].ToolCallId, ends[2].ToolCallId)
}

func TestACPReporter_StageAttribution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter, conn, client, _, cleanup := setupACPPair(t, ctx, cancel)
	defer cleanup()
	_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/workspace", McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	r := reporter.(*acpReporter)
	stage := "Subagent: Security"
	r.ReportStageIteration(stage, 2)
	r.ReportStageToolCalls(stage, 2, []llm.ToolCall{
		{ID: "subagent_0_iter_2_state", Name: "emit_reviewer_state", Arguments: `{"focus_area":"auth","message":"Checking permissions"}`},
		{ID: "subagent_0_iter_2_call", Name: "read_file", Arguments: `{"file_path":"auth.go"}`},
	})
	r.ReportStageEmptyResponseRetry(stage, 2, 1)
	r.ReportStageWarning(stage, "tool failed", errors.New("permission denied"))
	r.ReportStageCapReached(stage, 5)
	r.ReportStageTruncated(stage, 1024)
	r.ReportStageUsage(stage, llm.Usage{PromptTokens: 10, OutputTokens: 5})
	r.ReportStageUsage("AI Code Review", llm.Usage{PromptTokens: 20, OutputTokens: 10})

	var thoughts []string
	var calls []*acp.SessionUpdateToolCall
	var usages []*acp.SessionUsageUpdate
	require.Eventually(t, func() bool {
		thoughts, calls, usages = nil, nil, nil
		for _, notification := range client.snapshotUpdates() {
			u := notification.Update
			if u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil {
				thoughts = append(thoughts, u.AgentThoughtChunk.Content.Text.Text)
			}
			if u.ToolCall != nil {
				calls = append(calls, u.ToolCall)
			}
			if u.UsageUpdate != nil {
				usages = append(usages, u.UsageUpdate)
			}
		}
		return len(usages) == 2
	}, 2*time.Second, 5*time.Millisecond)
	require.Contains(t, thoughts, "[Subagent: Security] Iteration 2...\n")
	require.Contains(t, thoughts, "[Subagent: Security] [auth] Checking permissions\n")
	require.Contains(t, thoughts, "[Subagent: Security] Iteration 2: LLM returned empty response (attempt 1); retrying...\n")
	require.Contains(t, thoughts, "[Subagent: Security] Warning: tool failed: permission denied\n")
	require.Contains(t, thoughts, "[Subagent: Security] Reached maximum ReAct iterations (5). Forcing final review.\n")
	require.Contains(t, thoughts, "[Subagent: Security] LLM response truncated (hit max-tokens limit of 1024).\n")
	require.Len(t, calls, 1)
	require.Contains(t, calls[0].Title, "[Subagent: Security] read_file")
	require.Equal(t, stage, usages[0].Meta["stage"])
	require.Equal(t, "Security", usages[0].Meta["subagent"])
	require.Equal(t, "AI Code Review", usages[1].Meta["stage"])
	require.NotContains(t, usages[1].Meta, "subagent")
	require.EqualValues(t, 30, usages[1].Meta["cumulativeInputTokens"])
}

func TestACPReporter_CancelReviewWithoutActivePrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter, _, _, _, cleanup := setupACPPair(t, ctx, cancel)
	defer cleanup()
	r := reporter.(*acpReporter)
	reviewCtx, cancelReview := context.WithCancel(ctx)
	defer cancelReview()
	r.SetReviewCancel(cancelReview)
	require.NoError(t, r.Cancel(ctx, acp.CancelNotification{}))
	require.ErrorIs(t, reviewCtx.Err(), context.Canceled)
	require.NoError(t, ctx.Err(), "canceling review must leave session available")

	nextCtx, cancelNext := context.WithCancel(ctx)
	defer cancelNext()
	r.SetReviewCancel(cancelNext)
	r.SetReviewCancel(nil)
	require.NoError(t, r.Cancel(ctx, acp.CancelNotification{}))
	require.NoError(t, nextCtx.Err(), "cleared review cancellation must not be invoked")
}

func TestACPReporter_AskDeveloperSerializesAndCancelsQueuedQuestions(t *testing.T) {
	for _, cancellation := range []string{"caller", "session", "timeout"} {
		t.Run(cancellation, func(t *testing.T) {
			sessionCtx, cancelSession := context.WithCancel(context.Background())
			defer cancelSession()
			reporter, conn, client, _, cleanup := setupACPPair(t, sessionCtx, cancelSession)
			defer cleanup()
			entered := make(chan acp.UnstableCreateElicitationRequest, 3)
			release := make(chan struct{})
			defer close(release)
			client.elicitHandler = func(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
				select {
				case entered <- params:
				case <-ctx.Done():
					return acp.UnstableCreateElicitationResponse{}, ctx.Err()
				}
				select {
				case <-release:
					return acp.UnstableCreateElicitationResponse{Decline: &acp.UnstableCreateElicitationDecline{Action: "decline"}}, nil
				case <-ctx.Done():
					return acp.UnstableCreateElicitationResponse{}, ctx.Err()
				}
			}
			_, err := conn.Initialize(sessionCtx, acp.InitializeRequest{
				ProtocolVersion:    acp.ProtocolVersionNumber,
				ClientCapabilities: acp.ClientCapabilities{Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}},
			})
			require.NoError(t, err)
			r := reporter.(*acpReporter)
			firstCtx, cancelFirst := context.WithCancel(context.Background())
			defer cancelFirst()
			firstDone := make(chan error, 1)
			go func() {
				_, err := r.AskDeveloper(tools.WithSubagent(firstCtx, "Security"), tools.AskDeveloperRequest{Question: "First question?"})
				firstDone <- err
			}()
			select {
			case question := <-entered:
				require.Contains(t, question.Form.Message, "[Security] First question?")
				encoded, err := json.Marshal(question)
				require.NoError(t, err)
				require.Contains(t, string(encoded), "[Security] Clarification Request")
			case <-time.After(2 * time.Second):
				t.Fatal("first question did not reach client")
			}

			queuedCtx, cancelQueued := context.WithCancel(context.Background())
			defer cancelQueued()
			if cancellation == "timeout" {
				queuedCtx = tools.WithAskDeveloperTimeout(queuedCtx, 30*time.Millisecond)
			}
			type result struct {
				response tools.AskDeveloperResponse
				err      error
			}
			queuedDone := make(chan result, 1)
			go func() {
				response, err := r.AskDeveloper(queuedCtx, tools.AskDeveloperRequest{Question: "Queued question?"})
				queuedDone <- result{response: response, err: err}
			}()
			switch cancellation {
			case "caller":
				cancelQueued()
			case "session":
				cancelSession()
			}
			select {
			case got := <-queuedDone:
				if cancellation == "timeout" {
					require.NoError(t, got.err)
					require.Equal(t, "timeout", got.response.Status)
				} else {
					require.ErrorIs(t, got.err, context.Canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("queued question did not observe cancellation")
			}
			select {
			case <-entered:
				t.Fatal("queued question reached client while first question was active")
			default:
			}
			cancelFirst()
			select {
			case err := <-firstDone:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("active question did not observe cancellation")
			}
		})
	}
}

func TestACPConfigOptionsAdvertisesDefaultSlugWithConfiguredModelName(t *testing.T) {
	require.Nil(t, acpConfigOptions(""))

	opts := acpConfigOptions("claude-sonnet-5-5")
	require.Len(t, opts, 1)
	sel := opts[0].Select
	require.NotNil(t, sel)
	require.Equal(t, acp.SessionConfigValueId(acpDefaultModelID), sel.CurrentValue)
	require.NotNil(t, sel.Options.Ungrouped)
	require.Len(t, *sel.Options.Ungrouped, 1)
	require.Equal(t, acp.SessionConfigValueId(acpDefaultModelID), (*sel.Options.Ungrouped)[0].Value)
	require.Equal(t, "claude-sonnet-5-5", (*sel.Options.Ungrouped)[0].Name)
}

func TestACPReporter_RunSubagentCardAndIsolatedStages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter, conn, client, _, cleanup := setupACPPair(t, ctx, cancel)
	defer cleanup()
	_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/workspace", McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	r := reporter.(*acpReporter)

	require.Equal(t, acp.ToolKindThink, acpToolKinds[runSubagentToolName])

	mainCall := llm.ToolCall{ID: "call_1", Name: runSubagentToolName, Arguments: `{"title":"Trace auth","task":"find callers"}`}
	r.ReportToolCalls([]llm.ToolCall{mainCall})

	// Two sibling subagents with identical titles and identical provider IDs.
	var wg sync.WaitGroup
	var mu sync.Mutex
	for seq := 1; seq <= 2; seq++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sr := &subagentReporter{
				Reporter: r,
				mu:       &mu,
				prefix:   fmt.Sprintf("dynamic_subagent_%d_", seq),
				stage:    fmt.Sprintf("Subagent: Trace auth #%d", seq),
			}
			sr.ReportStageIteration(fmt.Sprintf("Trace auth #%d", seq), 1)
			call := llm.ToolCall{ID: "call_1", Name: "read_file", Arguments: `{"file_path":"auth.go"}`}
			sr.ReportToolCalls([]llm.ToolCall{call})
			sr.ReportToolStatus(call, "completed", nil)
			sr.ReportUsage(llm.Usage{PromptTokens: 3, OutputTokens: 1})
		}()
	}
	wg.Wait()
	r.ReportToolStatus(mainCall, "failed", errors.New("subagent failed"))

	var calls []*acp.SessionUpdateToolCall
	var updates []*acp.SessionToolCallUpdate
	var usages []*acp.SessionUsageUpdate
	require.Eventually(t, func() bool {
		calls, updates, usages = nil, nil, nil
		for _, n := range client.snapshotUpdates() {
			if n.Update.ToolCall != nil {
				calls = append(calls, n.Update.ToolCall)
			}
			if n.Update.ToolCallUpdate != nil {
				updates = append(updates, n.Update.ToolCallUpdate)
			}
			if n.Update.UsageUpdate != nil {
				usages = append(usages, n.Update.UsageUpdate)
			}
		}
		return len(calls) == 3 && len(updates) == 3 && len(usages) == 2
	}, 2*time.Second, 5*time.Millisecond)

	var mainCard *acp.SessionUpdateToolCall
	ids := map[acp.ToolCallId]bool{}
	for _, c := range calls {
		ids[c.ToolCallId] = true
		if c.Kind == acp.ToolKindThink {
			mainCard = c
		}
	}
	require.Len(t, ids, 3, "ACP call IDs must not collide")
	require.NotNil(t, mainCard)
	require.Equal(t, "Subagent: Trace auth", mainCard.Title)
	require.Equal(t, map[string]any{"title": "Trace auth", "task": "find callers"}, mainCard.RawInput)
	for _, u := range updates {
		if u.ToolCallId == mainCard.ToolCallId {
			require.Equal(t, acp.ToolCallStatusFailed, *u.Status)
			require.Equal(t, "subagent failed", u.RawOutput)
		} else {
			require.Equal(t, acp.ToolCallStatusCompleted, *u.Status)
		}
	}
	subagents := []any{usages[0].Meta["subagent"], usages[1].Meta["subagent"]}
	require.ElementsMatch(t, []any{"Trace auth #1", "Trace auth #2"}, subagents)
}
