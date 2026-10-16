package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

type testACPClient struct {
	mu      sync.Mutex
	updates []acp.SessionNotification
	updateC chan acp.SessionNotification
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

func (c *testACPClient) snapshotUpdates() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]acp.SessionNotification, len(c.updates))
	copy(out, c.updates)
	return out
}

func setupACPPair(t *testing.T, cancelSession context.CancelFunc) (Reporter, *acp.ClientSideConnection, *testACPClient, *bytes.Buffer, func()) {
	t.Helper()

	// agentToClient: agent writes to agentOutW, client reads from agentOutR
	agentOutR, agentOutW := io.Pipe()
	// clientToAgent: client writes to clientOutW, agent reads from clientOutR
	clientOutR, clientOutW := io.Pipe()

	var stderrBuf bytes.Buffer
	reporter := NewACPReporter(agentOutW, clientOutR, &stderrBuf, cancelSession)

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

func TestACPReporter_FullLifecycleAndPostReviewChat(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	reporter, clientConn, client, stderrBuf, cleanup := setupACPPair(t, cancelSession)
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
	require.Equal(t, defaultACPSessionID, newSessResp.SessionId)
	require.NotNil(t, newSessResp.Modes)
	require.Equal(t, acpModeReview, newSessResp.Modes.CurrentModeId)
	require.Len(t, newSessResp.Modes.AvailableModes, 2)

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

	reporter.ReportToolCalls([]llm.ToolCall{
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
	})
	reporter.ReportToolStatus("read_file", "started", nil)
	reporter.ReportToolStatus("read_file", "completed", nil)
	reporter.ReportToolStatus("grep_files", "failed", errors.New("grep error"))

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
		modeUpdates   []*acp.SessionCurrentModeUpdate
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
		if u.CurrentModeUpdate != nil {
			modeUpdates = append(modeUpdates, u.CurrentModeUpdate)
		}
		if u.Plan != nil {
			plans = append(plans, u.Plan)
		}
		if u.UsageUpdate != nil {
			usageUpdates = append(usageUpdates, u.UsageUpdate)
		}
	}

	require.Contains(t, thoughts, "[security] Checking auth middleware\n")
	require.Contains(t, messages, "Initial automated review findings.")
	require.Contains(t, messages, "First ACP chat reply")
	require.Contains(t, messages, "Second ACP chat reply")
	require.Len(t, toolStarts, 2)
	require.Equal(t, acp.ToolKindRead, toolStarts[0].Kind)
	require.Equal(t, acp.ToolKindSearch, toolStarts[1].Kind)
	require.Equal(t, 1, toolCompleted)
	require.Equal(t, 1, toolFailed)

	// Verify SessionInfoUpdate (title, metadata)
	require.NotEmpty(t, sessionInfos)
	require.NotNil(t, sessionInfos[0].Title)
	require.Contains(t, *sessionInfos[0].Title, "Code Review")
	require.Equal(t, "google", sessionInfos[0].Meta["provider"])

	// Verify mode transition from review to chat
	require.NotEmpty(t, modeUpdates)
	require.Equal(t, acpModeChat, modeUpdates[0].CurrentModeId)

	// Verify plan updates
	require.NotEmpty(t, plans)
	latestPlan := plans[len(plans)-1]
	require.NotEmpty(t, latestPlan.Entries)
	var foundChatPlan bool
	for _, entry := range latestPlan.Entries {
		if entry.Content == planStepChat && entry.Status == acp.PlanEntryStatusInProgress {
			foundChatPlan = true
		}
	}
	require.True(t, foundChatPlan, "expected plan to have chat step in progress")

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

	reporter, clientConn, _, _, cleanup := setupACPPair(t, cancelSession)
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
