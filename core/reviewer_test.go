package core

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
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

func TestReviewer_Run_PreReviewSummary(t *testing.T) {
	tmpDir := t.TempDir()
	preReviewPromptFile := filepath.Join(tmpDir, "pre-review-prompt.md")
	preReviewPromptContent := "Pre-review prompt content here."
	err := os.WriteFile(preReviewPromptFile, []byte(preReviewPromptContent), 0o644)
	require.NoError(t, err)

	cfg := config.NewDefaultConfig()
	cfg.PreReviewPromptFile = preReviewPromptFile

	lm := &mockLLM{
		responses: []*llm.Response{
			{
				Text: "This is the generated pre-review summary report.",
				Usage: llm.Usage{
					PromptTokens: 100,
					OutputTokens: 200,
				},
			},
			{
				Text: "This is the final AI code review.",
				Usage: llm.Usage{
					PromptTokens: 1000,
					OutputTokens: 2000,
				},
			},
		},
	}
	dispatcher := newMockDispatcher()
	spy := &spyReporter{}

	reviewer := &Reviewer{
		Agent:                    NewAgent(lm, dispatcher, WithReporter(spy)),
		Config:                   cfg,
		StablePrompt:             "stable system review instructions",
		Guidelines:               "general rules",
		RootDir:                  tmpDir,
		ApprovalEvaluationPrompt: "approval rules",
	}

	result, err := reviewer.Run(context.Background(), []string{"file1.go"}, "original request text")
	require.NoError(t, err)
	require.Equal(t, "This is the final AI code review.", result)

	// Verify that the LLM was called exactly twice
	require.Equal(t, 2, len(lm.calls))

	// Verify the first call (Pre-Review Summary phase)
	firstCallMsgs := lm.calls[0]
	// The first message is the stable pre-review prompt (Zone 1)
	require.Equal(t, llm.RoleSystem, firstCallMsgs[0].Role)
	require.Equal(t, preReviewPromptContent, firstCallMsgs[0].Text)
	// The user request was passed as input to the pre-review phase
	require.Equal(t, llm.RoleUser, firstCallMsgs[len(firstCallMsgs)-1].Role)
	require.Equal(t, "original request text", firstCallMsgs[len(firstCallMsgs)-1].Text)

	// Verify the second call (AI Code Review phase)
	secondCallMsgs := lm.calls[1]
	// The user request in the second phase should contain the pre-review summary prepended
	lastMsg := secondCallMsgs[len(secondCallMsgs)-1]
	require.Equal(t, llm.RoleUser, lastMsg.Role)
	require.Contains(t, lastMsg.Text, "### Pre-Review Summary")
	require.Contains(t, lastMsg.Text, "This is the generated pre-review summary report.")
	require.Contains(t, lastMsg.Text, "original request text")

	// Verify that stage transitions were reported correctly
	require.Equal(t, 2, len(spy.stageIterations))
	require.Equal(t, "Pre-Review Summary", spy.stageIterations[0].stage)
	require.Equal(t, 1, spy.stageIterations[0].iter)
	require.Equal(t, "AI Code Review", spy.stageIterations[1].stage)
	require.Equal(t, 1, spy.stageIterations[1].iter)

	require.Equal(t, 2, len(spy.stageFinalReviews))
	require.Equal(t, "Pre-Review Summary", spy.stageFinalReviews[0])
	require.Equal(t, "AI Code Review", spy.stageFinalReviews[1])

	// Verify that metrics were collected for both phases independently
	metrics := reviewer.GetMetrics()
	require.Equal(t, 2, len(metrics.Phases))

	require.Equal(t, "pre_review", metrics.Phases[0].Phase)
	require.Equal(t, 100, metrics.Phases[0].Metrics.Tokens.Input)
	require.Equal(t, 200, metrics.Phases[0].Metrics.Tokens.Output)
	require.Equal(t, 1, metrics.Phases[0].Metrics.Iterations)

	require.Equal(t, "review", metrics.Phases[1].Phase)
	require.Equal(t, 1000, metrics.Phases[1].Metrics.Tokens.Input)
	require.Equal(t, 2000, metrics.Phases[1].Metrics.Tokens.Output)
	require.Equal(t, 1, metrics.Phases[1].Metrics.Iterations)
}

func TestReviewer_Run_PreReviewModelOverride(t *testing.T) {
	tmpDir := t.TempDir()
	preReviewPromptFile := filepath.Join(tmpDir, "pre-review-prompt.md")
	preReviewPromptContent := "Pre-review prompt."
	err := os.WriteFile(preReviewPromptFile, []byte(preReviewPromptContent), 0o644)
	require.NoError(t, err)

	cfg := config.NewDefaultConfig()
	cfg.PreReviewPromptFile = preReviewPromptFile
	cfg.Model = "main-model"
	cfg.PreReviewModel = "pre-review-model-override"
	cfg.Provider = "google"

	lm := &mockLLM{
		responses: []*llm.Response{
			textResponse("This is the final AI code review."),
		},
	}
	preLLM := &mockLLM{
		responses: []*llm.Response{
			textResponse("This is the generated pre-review summary report from overridden model."),
		},
	}

	dispatcher := newMockDispatcher()
	spy := &spyReporter{}

	reviewer := &Reviewer{
		Agent:                    NewAgent(lm, dispatcher, WithReporter(spy)),
		Config:                   cfg,
		StablePrompt:             "stable system review instructions",
		Guidelines:               "general rules",
		RootDir:                  tmpDir,
		ApprovalEvaluationPrompt: "approval rules",
	}

	// Setup custom factory to return preLLM when pre-review-model-override is requested
	var preReviewModelUsed bool
	reviewer.llmFactory = func(ctx context.Context, provider, modelName, apiKey, baseURL string, options map[string]any) (llm.Model, error) {
		if modelName == "pre-review-model-override" {
			preReviewModelUsed = true
			return preLLM, nil
		}
		return lm, nil
	}

	result, err := reviewer.Run(context.Background(), []string{"file1.go"}, "original request text")
	require.NoError(t, err)
	require.Equal(t, "This is the final AI code review.", result)

	// Verify that the custom model was indeed constructed and used
	require.True(t, preReviewModelUsed, "The pre-review model override should have been constructed")
	require.Equal(t, 1, len(preLLM.calls))
	require.Equal(t, 1, len(lm.calls))

	// Verify the request to the main model contains the pre-review summary from the override model
	require.Contains(t, lm.calls[0][len(lm.calls[0])-1].Text, "This is the generated pre-review summary report from overridden model.")
}
