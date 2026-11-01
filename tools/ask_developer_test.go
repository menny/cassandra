package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

type mockNotifier struct {
	notified int
}

func (m *mockNotifier) NotifyUser() {
	m.notified++
}

type mockPrompter struct {
	lastReq AskDeveloperRequest
	resp    AskDeveloperResponse
	err     error
}

func (m *mockPrompter) AskDeveloper(ctx context.Context, req AskDeveloperRequest) (AskDeveloperResponse, error) {
	if err := ctx.Err(); err != nil {
		return AskDeveloperResponse{}, err
	}
	m.lastReq = req
	return m.resp, m.err
}

func TestAskDeveloper_Answered(t *testing.T) {
	r := NewRegistry()
	notifier := mockNotifier{}
	registerAskDeveloper(r, nil, &notifier)

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// Feed answer in a goroutine
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("My Answer\n"))
	}()

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:  "Should we use PostgreSQL?",
		Reasoning: "It affects the database abstraction layer.",
	})
	require.NoError(t, err)

	res, err := r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(res), &payload))
	require.Equal(t, "answered", payload["status"])
	require.Equal(t, "My Answer", payload["response"])
	require.Equal(t, 1, notifier.notified)
}

func TestAskDeveloper_Skipped(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, nil, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// Feed empty answer
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("\n"))
	}()

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:  "Should we use PostgreSQL?",
		Reasoning: "It affects the database abstraction layer.",
	})
	require.NoError(t, err)

	res, err := r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(res), &payload))
	require.Equal(t, "skipped", payload["status"])
	require.Contains(t, payload["message"], "The developer did not respond.")
}

func TestAskDeveloper_Timeout(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, nil, &mockNotifier{})

	inR, inW := io.Pipe()
	defer inW.Close()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)
	ctx = WithAskDeveloperTimeout(ctx, 50*time.Millisecond)

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:  "Should we use PostgreSQL?",
		Reasoning: "It affects the database abstraction layer.",
	})
	require.NoError(t, err)

	// The form will wait, and then exit due to context timeout
	res, err := r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(res), &payload))
	require.Equal(t, "timeout", payload["status"])
	require.Contains(t, payload["message"], "The developer did not respond within")
}

func TestAskDeveloper_Cancelled(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, nil, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	cancelCtx, cancel := context.WithCancel(context.Background())

	ctx := WithTestStreams(cancelCtx, inR, &outBuf)

	// Cancel the context in a goroutine while the form is running
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		inW.Close()
	}()

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:  "Should we use PostgreSQL?",
		Reasoning: "It affects the database abstraction layer.",
	})
	require.NoError(t, err)

	_, err = r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAskDeveloper_CustomPrompter(t *testing.T) {
	r := NewRegistry()
	notifier := mockNotifier{}
	prompter := &mockPrompter{
		resp: AskDeveloperResponse{
			Status:   "answered",
			Selected: []string{"PostgreSQL"},
			Response: "Use PostgreSQL for ACID compliance.",
		},
	}
	r.SetDeveloperPrompter(prompter)
	registerAskDeveloper(r, prompter, &notifier)

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:      "Which database should we use?",
		Reasoning:     "We need a persistent store.",
		Options:       []string{"PostgreSQL", "SQLite", "MySQL"},
		IsMultiSelect: false,
	})
	require.NoError(t, err)

	res, err := r.HandleCall(context.Background(), llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var resp AskDeveloperResponse
	require.NoError(t, json.Unmarshal([]byte(res), &resp))
	require.Equal(t, "answered", resp.Status)
	require.Equal(t, []string{"PostgreSQL"}, resp.Selected)
	require.Equal(t, "Use PostgreSQL for ACID compliance.", resp.Response)
	require.Equal(t, 1, notifier.notified)
	require.Equal(t, "Which database should we use?", prompter.lastReq.Question)
	require.Equal(t, []string{"PostgreSQL", "SQLite", "MySQL"}, prompter.lastReq.Options)
	require.False(t, prompter.lastReq.IsMultiSelect)
}

func TestAskDeveloper_TerminalPrompter_Options(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, nil, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// In accessible mode:
	// Choice 1: "SQLite"
	// Choice 2: "PostgreSQL"
	// Choice 3: "Other (specify below)"
	// Then input field: "Additional notes"
	go func() {
		defer inW.Close()
		// Select option 1 (SQLite)
		_, _ = inW.Write([]byte("1\n"))
		// Wait for select prompt to finish and second input prompt to start reading
		time.Sleep(30 * time.Millisecond)
		_, _ = inW.Write([]byte("Local development notes\n"))
	}()

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:      "Which database should we use?",
		Reasoning:     "Persistence choice.",
		Options:       []string{"SQLite", "PostgreSQL"},
		IsMultiSelect: false,
	})
	require.NoError(t, err)

	res, err := r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var resp AskDeveloperResponse
	require.NoError(t, json.Unmarshal([]byte(res), &resp))
	require.Equal(t, "answered", resp.Status)
	require.Equal(t, []string{"SQLite"}, resp.Selected)
	require.Equal(t, "Local development notes", resp.Response)
}

func TestAskDeveloper_TerminalPrompter_OtherChoice(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, nil, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// Choice 1: "SQLite"
	// Choice 2: "PostgreSQL"
	// Choice 3: "Other (specify below)"
	go func() {
		defer inW.Close()
		// Select "Other" (3)
		_, _ = inW.Write([]byte("3\n"))
		time.Sleep(30 * time.Millisecond)
		// Enter custom choice in response
		_, _ = inW.Write([]byte("DuckDB\n"))
	}()

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:      "Which database should we use?",
		Reasoning:     "Persistence choice.",
		Options:       []string{"SQLite", "PostgreSQL"},
		IsMultiSelect: false,
	})
	require.NoError(t, err)

	res, err := r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var resp AskDeveloperResponse
	require.NoError(t, json.Unmarshal([]byte(res), &resp))
	require.Equal(t, "answered", resp.Status)
	require.Empty(t, resp.Selected)
	require.Equal(t, "DuckDB", resp.Response)
}

func TestAskDeveloper_TerminalPrompter_MultiSelect(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, nil, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// In accessible mode for multiselect:
	// Enter "1\n" to select item 1
	// Enter "0\n" to finish selection
	// Then enter input text
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("1\n"))
		time.Sleep(30 * time.Millisecond)
		_, _ = inW.Write([]byte("0\n"))
		time.Sleep(30 * time.Millisecond)
		_, _ = inW.Write([]byte("Selected option 1 details\n"))
	}()

	argsBytes, err := json.Marshal(AskDeveloperRequest{
		Question:      "Which tags should apply?",
		Reasoning:     "Tag classification.",
		Options:       []string{"backend", "frontend", "infra"},
		IsMultiSelect: true,
	})
	require.NoError(t, err)

	res, err := r.HandleCall(ctx, llm.ToolCall{
		Name:      "ask_developer",
		Arguments: string(argsBytes),
	})
	require.NoError(t, err)

	var resp AskDeveloperResponse
	require.NoError(t, json.Unmarshal([]byte(res), &resp))
	require.Equal(t, "answered", resp.Status)
	require.Equal(t, []string{"backend"}, resp.Selected)
	require.Equal(t, "Selected option 1 details", resp.Response)
}
