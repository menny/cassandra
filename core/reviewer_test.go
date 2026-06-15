package core

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestReviewer_RunInteractivePostReview_ExitCommands(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"exit lowercase", "exit\n.\n"},
		{"exit uppercase", "EXIT\n.\n"},
		{"bye", "bye\n.\n"},
		{"/exit", "/exit\n.\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefaultConfig()
			cfg.Render = "markdown"

			lm := &mockLLM{
				responses: []*llm.Response{
					textResponse("automated review content"),
				},
			}
			dispatcher := newMockDispatcher()
			spy := &spyReporter{}

			reviewer := &Reviewer{
				Agent:  NewAgent(lm, dispatcher, WithReporter(spy)),
				Config: cfg,
			}

			// Pre-populate history as if RunReview had run
			reviewer.Agent.history = []llm.Message{
				{Role: llm.RoleSystem, Text: "stable system"},
				{Role: llm.RoleUser, Text: "request review"},
				{Role: llm.RoleAssistant, Text: "automated review content"},
			}

			inR, inW := io.Pipe()
			var outBuf bytes.Buffer

			ctx := WithTestREPLStreams(context.Background(), inR, &outBuf)

			// Feed the exit command immediately
			go func() {
				defer inW.Close()
				_, _ = inW.Write([]byte(tt.input))
			}()

			err := reviewer.RunInteractivePostReview(ctx)
			require.NoError(t, err)

			// History must contain the post-review system instruction
			require.NotEmpty(t, reviewer.Agent.history)
			var postReviewSystemSeen bool
			for _, msg := range reviewer.Agent.history {
				if msg.Role == llm.RoleSystem && msg.Text == postReviewSystemInstruction {
					postReviewSystemSeen = true
					break
				}
			}
			require.True(t, postReviewSystemSeen, "postReviewSystemInstruction must be in history")
		})
	}
}

func TestReviewer_RunInteractivePostReview_ChatFlight(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.Render = "markdown"

	lm := &mockLLM{
		responses: []*llm.Response{
			textResponse("Cassandra answer to query 1"),
		},
	}
	dispatcher := newMockDispatcher()
	spy := &spyReporter{}

	reviewer := &Reviewer{
		Agent:  NewAgent(lm, dispatcher, WithReporter(spy)),
		Config: cfg,
	}

	// Pre-populate history
	reviewer.Agent.history = []llm.Message{
		{Role: llm.RoleSystem, Text: "stable system"},
		{Role: llm.RoleUser, Text: "request review"},
		{Role: llm.RoleAssistant, Text: "automated review content"},
	}

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer

	ctx := WithTestREPLStreams(context.Background(), inR, &outBuf)

	// Feed a query and then exit
	go func() {
		defer inW.Close()
		_, _ = inW.Write([]byte("why did you flag this file?\n.\n"))
		// Wait a bit to let it process, then send exit
		time.Sleep(50 * time.Millisecond)
		_, _ = inW.Write([]byte("exit\n.\n"))
	}()

	err := reviewer.RunInteractivePostReview(ctx)
	require.NoError(t, err)

	// Verify LLM calls were made with the query
	require.Equal(t, 1, len(lm.calls))
	lastCall := lm.calls[len(lm.calls)-1]

	// Find the user query in the captured messages
	var userQuerySeen bool
	for _, msg := range lastCall {
		if msg.Role == llm.RoleUser && msg.Text == "why did you flag this file?" {
			userQuerySeen = true
		}
	}
	require.True(t, userQuerySeen, "User query must be passed in LLM context")

	// Verify history has both user query and Cassandra reply
	var historyUserSeen bool
	var historyAssistantSeen bool
	for _, msg := range reviewer.Agent.history {
		if msg.Role == llm.RoleUser && msg.Text == "why did you flag this file?" {
			historyUserSeen = true
		}
		if msg.Role == llm.RoleAssistant && msg.Text == "Cassandra answer to query 1" {
			historyAssistantSeen = true
		}
	}
	require.True(t, historyUserSeen, "User query must be appended to history")
	require.True(t, historyAssistantSeen, "Cassandra answer must be appended to history")
}

func TestReviewer_RunInteractivePostReview_Cancellation(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.Render = "markdown"

	lm := &mockLLM{}
	dispatcher := newMockDispatcher()
	spy := &spyReporter{}

	reviewer := &Reviewer{
		Agent:  NewAgent(lm, dispatcher, WithReporter(spy)),
		Config: cfg,
	}

	inR, inW := io.Pipe()
	defer inW.Close()
	var outBuf bytes.Buffer

	cancelCtx, cancel := context.WithCancel(context.Background())
	ctx := WithTestREPLStreams(cancelCtx, inR, &outBuf)

	// Cancel the context while running
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := reviewer.RunInteractivePostReview(ctx)
	// Context cancellation should break the loop cleanly returning nil error
	require.NoError(t, err)
}
