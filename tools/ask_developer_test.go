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

func TestAskDeveloper_Answered(t *testing.T) {
	r := NewRegistry()
	notifier := mockNotifier{}
	registerAskDeveloper(r, &notifier)

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// Feed answer in a goroutine
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("My Answer\n"))
	}()

	argsBytes, err := json.Marshal(askDeveloperArgs{
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
	registerAskDeveloper(r, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)

	// Feed empty answer
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("\n"))
	}()

	argsBytes, err := json.Marshal(askDeveloperArgs{
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
	registerAskDeveloper(r, &mockNotifier{})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestStreams(context.Background(), inR, &outBuf)
	ctx = WithAskDeveloperTimeout(ctx, 50*time.Millisecond)

	argsBytes, err := json.Marshal(askDeveloperArgs{
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

	inW.Close()
}

func TestAskDeveloper_Cancelled(t *testing.T) {
	r := NewRegistry()
	registerAskDeveloper(r, &mockNotifier{})

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

	argsBytes, err := json.Marshal(askDeveloperArgs{
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
