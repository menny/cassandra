package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
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
			{
				Text: "Cassandra answer to query 1",
				Usage: llm.Usage{
					PromptTokens: 300,
					OutputTokens: 400,
				},
			},
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

	// Verify that discussion metrics were tracked correctly
	metrics := reviewer.GetMetrics()
	require.Equal(t, 1, len(metrics.Phases))

	require.Equal(t, "review_discussion", metrics.Phases[0].Phase)
	require.Equal(t, 300, metrics.Phases[0].Metrics.Tokens.Input)
	require.Equal(t, 400, metrics.Phases[0].Metrics.Tokens.Output)
	require.Equal(t, 1, metrics.Phases[0].Metrics.Iterations)
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

func TestReviewer_Run_PipelineSummary(t *testing.T) {
	tmpDir := t.TempDir()
	preReviewPromptContent := "Summary instructions."
	cfg := config.NewDefaultConfig()

	lm := &mockLLM{
		responses: []*llm.Response{
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

	subLLM := &mockLLM{responses: []*llm.Response{{Text: "Summary report.", Usage: llm.Usage{PromptTokens: 100, OutputTokens: 200}}}}
	reviewer.Pipeline = &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{{
		Title: "Summary", Description: "Summarize changes", Prompt: preReviewPromptContent, UserVisible: true, ReviewerInput: true,
	}}}
	reviewer.llmFactory = func(ctx context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
		return subLLM, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := reviewer.Run(ctx, []string{"file1.go"}, "original request text")
	require.NoError(t, err)
	require.Equal(t, "This is the final AI code review.", result)

	require.Len(t, lm.calls, 1)
	require.Len(t, subLLM.calls, 1)
	firstCallMsgs := subLLM.calls[0]
	// The first message is the stable pre-review prompt (Zone 1)
	require.Equal(t, llm.RoleSystem, firstCallMsgs[0].Role)
	require.Equal(t, preReviewPromptContent, firstCallMsgs[0].Text)
	// The user request was passed as input to the pre-review phase
	require.Equal(t, llm.RoleUser, firstCallMsgs[len(firstCallMsgs)-1].Role)
	require.Equal(t, "original request text", firstCallMsgs[len(firstCallMsgs)-1].Text)

	// Verify the second call (AI Code Review phase)
	secondCallMsgs := lm.calls[0]
	// Reports follow the original request.
	lastMsg := secondCallMsgs[len(secondCallMsgs)-1]
	require.Equal(t, llm.RoleUser, lastMsg.Role)
	reports := pipelineReports(t, "original request text", lastMsg.Text)
	require.Equal(t, []map[string]string{{"title": "Summary", "description": "Summarize changes", "output": "Summary report."}}, reports)

	// Verify that stage transitions were reported correctly
	require.Equal(t, 2, len(spy.stageIterations))
	require.Equal(t, "Subagent: Summary", spy.stageIterations[0].stage)
	require.Equal(t, 1, spy.stageIterations[0].iter)
	require.Equal(t, "AI Code Review", spy.stageIterations[1].stage)
	require.Equal(t, 1, spy.stageIterations[1].iter)

	require.Equal(t, 2, len(spy.stageFinalReviews))
	require.Equal(t, "Subagent: Summary", spy.stageFinalReviews[0])
	require.Equal(t, "AI Code Review", spy.stageFinalReviews[1])

	// Verify that metrics were collected for both phases independently
	metrics := reviewer.GetMetrics()
	require.Equal(t, 2, len(metrics.Phases))

	require.Equal(t, "subagent", metrics.Phases[0].Phase)
	require.Equal(t, "Summary", metrics.Phases[0].Title)
	require.Equal(t, 100, metrics.Phases[0].Metrics.Tokens.Input)
	require.Equal(t, 200, metrics.Phases[0].Metrics.Tokens.Output)
	require.Equal(t, 1, metrics.Phases[0].Metrics.Iterations)

	require.Equal(t, "review", metrics.Phases[1].Phase)
	require.Equal(t, 1000, metrics.Phases[1].Metrics.Tokens.Input)
	require.Equal(t, 2000, metrics.Phases[1].Metrics.Tokens.Output)
	require.Equal(t, 1, metrics.Phases[1].Metrics.Iterations)
}

func TestReviewer_Run_PipelineModelOverride(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	cfg.Model = "main-model"
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

	reviewer.Pipeline = &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{{
		Title: "Summary", Prompt: "Summary instructions", ReviewerInput: true,
		LLM: config.LLMOverrides{Model: stringPointer("pre-review-model-override")},
	}}}
	var preReviewModelUsed bool
	reviewer.llmFactory = func(ctx context.Context, provider, modelName, apiKey, baseURL string, options map[string]any) (llm.Model, error) {
		if modelName == "pre-review-model-override" {
			preReviewModelUsed = true
			return preLLM, ctx.Err()
		}
		return nil, errors.New("unexpected subagent model")
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

func TestReviewer_MetricsPreservedOnError(t *testing.T) {
	t.Run("RequiredSubagentError", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := config.NewDefaultConfig()
		cfg.Provider = "google"

		lm := &mockLLM{
			responses: []*llm.Response{
				{
					Text: "Pre-review response requesting tool",
					ToolCalls: []llm.ToolCall{
						{
							ID:        "call1",
							Name:      "read_file",
							Arguments: `{"file_path":"file1.go"}`,
						},
					},
					Usage: llm.Usage{
						PromptTokens: 50,
						OutputTokens: 100,
					},
				},
			},
		}

		reviewer := &Reviewer{
			Agent:                    NewAgent(lm, newMockDispatcher(), WithReporter(&spyReporter{})),
			Config:                   cfg,
			StablePrompt:             "stable system review instructions",
			Guidelines:               "general rules",
			RootDir:                  tmpDir,
			ApprovalEvaluationPrompt: "approval rules",
		}

		main := &mockLLM{}
		reviewer.Agent.llm = main
		reviewer.Pipeline = &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{{Title: "Required", Prompt: "instructions", Required: true}}}
		reviewer.llmFactory = func(ctx context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
			return lm, ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := reviewer.Run(ctx, []string{"file1.go"}, "original request text")
		require.Empty(t, main.calls)
		require.Error(t, err)

		metrics := reviewer.GetMetrics()
		require.Equal(t, 1, len(metrics.Phases))
		require.Equal(t, "subagent", metrics.Phases[0].Phase)
		require.Equal(t, "Required", metrics.Phases[0].Title)
		require.Equal(t, 50, metrics.Phases[0].Metrics.Tokens.Input)
		require.Equal(t, 100, metrics.Phases[0].Metrics.Tokens.Output)
	})

	t.Run("CodeReviewError", func(t *testing.T) {
		cfg := config.NewDefaultConfig()
		cfg.Provider = "google"

		lm := &mockLLM{
			responses: []*llm.Response{
				{
					Text: "Review response requesting tool",
					ToolCalls: []llm.ToolCall{
						{
							ID:        "call1",
							Name:      "read_file",
							Arguments: `{"file_path":"file1.go"}`,
						},
					},
					Usage: llm.Usage{
						PromptTokens: 500,
						OutputTokens: 1000,
					},
				},
			},
		}

		reviewer := &Reviewer{
			Agent:                    NewAgent(lm, newMockDispatcher(), WithReporter(&spyReporter{})),
			Config:                   cfg,
			StablePrompt:             "stable system review instructions",
			Guidelines:               "general rules",
			RootDir:                  t.TempDir(),
			ApprovalEvaluationPrompt: "approval rules",
		}

		_, err := reviewer.Run(context.Background(), []string{"file1.go"}, "original request text")
		require.Error(t, err)

		metrics := reviewer.GetMetrics()
		require.Equal(t, 1, len(metrics.Phases))
		require.Equal(t, "review", metrics.Phases[0].Phase)
		require.Equal(t, 500, metrics.Phases[0].Metrics.Tokens.Input)
		require.Equal(t, 1000, metrics.Phases[0].Metrics.Tokens.Output)
	})

	t.Run("ChatFlightError", func(t *testing.T) {
		cfg := config.NewDefaultConfig()
		cfg.Render = "markdown"

		lm := &mockLLM{
			responses: []*llm.Response{
				{
					Text: "Chat response requesting tool",
					ToolCalls: []llm.ToolCall{
						{
							ID:        "call1",
							Name:      "read_file",
							Arguments: `{"file_path":"file1.go"}`,
						},
					},
					Usage: llm.Usage{
						PromptTokens: 20,
						OutputTokens: 30,
					},
				},
			},
		}

		reviewer := &Reviewer{
			Agent:                    NewAgent(lm, newMockDispatcher(), WithReporter(&spyReporter{})),
			Config:                   cfg,
			StablePrompt:             "stable system review instructions",
			Guidelines:               "general rules",
			RootDir:                  t.TempDir(),
			ApprovalEvaluationPrompt: "approval rules",
		}

		ctx := context.WithValue(context.Background(), replStdinKey{}, strings.NewReader("query 1\n"))
		ctx = context.WithValue(ctx, replStderrKey{}, io.Discard)

		err := reviewer.RunInteractivePostReview(ctx)
		require.Error(t, err)

		metrics := reviewer.GetMetrics()
		require.Equal(t, 1, len(metrics.Phases))
		require.Equal(t, "review_discussion", metrics.Phases[0].Phase)
		require.Equal(t, 20, metrics.Phases[0].Metrics.Tokens.Input)
		require.Equal(t, 30, metrics.Phases[0].Metrics.Tokens.Output)
	})
}

func TestReviewer_RunInteractivePostReview_MultiTurnMetrics(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.Render = "markdown"

	lm := &mockLLM{
		responses: []*llm.Response{
			{
				Text: "Reply 1",
				Usage: llm.Usage{
					PromptTokens: 100,
					OutputTokens: 50,
				},
			},
			{
				Text: "Reply 2",
				Usage: llm.Usage{
					PromptTokens: 150,
					OutputTokens: 80,
				},
			},
		},
	}

	reviewer := &Reviewer{
		Agent:                    NewAgent(lm, newMockDispatcher(), WithReporter(&spyReporter{})),
		Config:                   cfg,
		StablePrompt:             "stable system review instructions",
		Guidelines:               "general rules",
		RootDir:                  t.TempDir(),
		ApprovalEvaluationPrompt: "approval rules",
	}

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("query 1\n"))
		time.Sleep(20 * time.Millisecond)
		_, _ = pw.Write([]byte("query 2\n"))
		time.Sleep(20 * time.Millisecond)
		_, _ = pw.Write([]byte("exit\n"))
		_ = pw.Close()
	}()

	ctx := context.WithValue(context.Background(), replStdinKey{}, pr)
	ctx = context.WithValue(ctx, replStderrKey{}, io.Discard)

	err := reviewer.RunInteractivePostReview(ctx)
	require.NoError(t, err)

	metrics := reviewer.GetMetrics()
	require.Equal(t, 1, len(metrics.Phases))
	require.Equal(t, "review_discussion", metrics.Phases[0].Phase)
	require.Equal(t, 250, metrics.Phases[0].Metrics.Tokens.Input)
	require.Equal(t, 130, metrics.Phases[0].Metrics.Tokens.Output)
	require.Equal(t, 2, metrics.Phases[0].Metrics.Iterations)
}

func TestReviewer_Run_PipelineErrors(t *testing.T) {
	t.Run("PipelineFileReadError", func(t *testing.T) {
		cfg := config.NewDefaultConfig()
		cfg.ReviewPipelineFile = filepath.Join(t.TempDir(), "missing.json")

		reviewer := &Reviewer{
			Agent:      NewAgent(&mockLLM{}, newMockDispatcher(), WithReporter(&spyReporter{})),
			Config:     cfg,
			Guidelines: "general guidelines",
			RootDir:    t.TempDir(),
		}

		_, err := reviewer.Run(context.Background(), []string{"file1.go"}, "req")
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing.json")
	})

	t.Run("RequiredSubagentLLMFactoryError", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := config.NewDefaultConfig()
		cfg.Model = "model-main"

		originalLM := &mockLLM{}
		reviewer := &Reviewer{
			Agent:      NewAgent(originalLM, newMockDispatcher(), WithReporter(&spyReporter{})),
			Config:     cfg,
			Guidelines: "general guidelines",
			RootDir:    tmpDir,
			llmFactory: func(ctx context.Context, provider, modelName, apiKey, baseURL string, options map[string]any) (llm.Model, error) {
				return nil, errors.New("factory init failure")
			},
		}

		reviewer.Pipeline = &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{{Title: "Required", Prompt: "instructions", Required: true}}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := reviewer.Run(ctx, []string{"file1.go"}, "req")
		require.Error(t, err)
		require.Contains(t, err.Error(), "factory init failure")
		require.Empty(t, originalLM.calls)
		require.Equal(t, originalLM, reviewer.Agent.llm)
	})
}

type pipelineFocusReporter struct {
	pipelineReporter
	waits int
}

func (s *pipelineFocusReporter) WaitForFirstMessage(ctx context.Context) (string, error) {
	s.waits++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "explicit focus", nil
}

func (s *pipelineFocusReporter) ServePostReview(ctx context.Context, handler func(context.Context, string) error) error {
	return handler(ctx, "discussion")
}

func (s *pipelineFocusReporter) SignalReviewFailed(err error) error { return err }

func TestReviewer_PipelineACPWaitIsExplicit(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(fmt.Sprintf("wait=%t", wait), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, main, _ := newPipelineReviewer(t, &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{{Title: "Summary", Prompt: "instructions", UserVisible: true, ReviewerInput: true}}})
			r.Config.Render = "acp"
			r.Config.WaitForMessage = wait
			reporter := &pipelineFocusReporter{}
			r.Agent.reporter = reporter
			sub := &mockLLM{responses: []*llm.Response{textResponse("summary")}}
			r.llmFactory = func(ctx context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
				return sub, ctx.Err()
			}
			_, err := r.Run(ctx, nil, "request")
			require.NoError(t, err)
			require.Len(t, main.calls, 1)
			require.Len(t, sub.calls, 1)
			if wait {
				require.Equal(t, 1, reporter.waits, "only the explicit initial wait is allowed")
				require.Contains(t, sub.calls[0][len(sub.calls[0])-1].Text, "explicit focus")
				require.Contains(t, main.calls[0][len(main.calls[0])-1].Text, "explicit focus")
			} else {
				require.Zero(t, reporter.waits, "pipeline completion must not implicitly wait for ACP focus")
			}
		})
	}
}

func TestReviewer_PipelineFocusReporterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	reporter := &pipelineFocusReporter{}
	_, err := reporter.WaitForFirstMessage(ctx)
	require.ErrorIs(t, err, context.Canceled)
	err = reporter.ServePostReview(ctx, func(ctx context.Context, query string) error {
		require.Equal(t, "discussion", query)
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestReviewer_Run_CancelledBeforeWaitForMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	r, main, _ := newPipelineReviewer(t, &config.ReviewPipeline{MaxConcurrency: 1, Subagents: []config.Subagent{
		{Title: "never", Prompt: "instructions"},
	}})
	r.Config.Render = "acp"
	r.Config.WaitForMessage = true
	reporter := &pipelineSafetyReporter{cancelUpdates: make(chan context.CancelFunc, 2)}
	r.Agent.reporter = reporter
	sub := &mockLLM{responses: []*llm.Response{textResponse("must not run")}}
	factoryCalls := 0
	r.llmFactory = func(ctx context.Context, _, _, _, _ string, _ map[string]any) (llm.Model, error) {
		factoryCalls++
		return sub, ctx.Err()
	}

	output, err := r.Run(ctx, nil, "request")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, output)
	require.Zero(t, reporter.waits, "already-cancelled Run must not enter the initial message wait")
	require.Zero(t, factoryCalls)
	require.Empty(t, sub.calls)
	require.Empty(t, main.calls)
	require.NotNil(t, receivePipeline(t, reporter.cancelUpdates))
	require.Nil(t, receivePipeline(t, reporter.cancelUpdates), "even an early return must clear its cancel callback")
}

func TestReviewer_ExecutePostReviewTurn_RollbackHistoryOnError(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.Render = "acp"

	errorLLM := &mockLLM{errs: []error{errors.New("provider 500 error")}}
	dispatcher := newMockDispatcher()
	spy := &spyReporter{}

	reviewer := &Reviewer{
		Agent:  NewAgent(errorLLM, dispatcher, WithReporter(spy)),
		Config: cfg,
	}

	reviewer.Agent.history = []llm.Message{
		{Role: llm.RoleSystem, Text: "system prompt"},
		{Role: llm.RoleAssistant, Text: "initial review"},
	}
	initialLen := len(reviewer.Agent.history)

	err := reviewer.executePostReviewTurn(context.Background(), "Why this?", 5, nil)
	require.Error(t, err)
	require.Equal(t, initialLen, len(reviewer.Agent.history), "history should roll back to pre-turn length on error")
	require.Equal(t, llm.RoleAssistant, reviewer.Agent.history[len(reviewer.Agent.history)-1].Role)
}

func TestReviewer_PostReview_UpdateReviewVerdict(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.Render = "markdown"

	registry := tools.NewRegistry()
	tools.RegisterLocalTools(registry, t.TempDir(), "main", "HEAD", nil, "", false, false, nil, nil)

	argsBytes, err := json.Marshal(map[string]any{
		"verdict":   "approved",
		"rationale": "Author explained why the edge case is already handled.",
	})
	require.NoError(t, err)

	lm := &mockLLM{
		responses: []*llm.Response{
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:        "call_1",
						Name:      "update_review_verdict",
						Arguments: string(argsBytes),
					},
				},
			},
			{
				Text: "I have updated the review verdict to Approved based on your explanation.",
			},
		},
	}

	spy := &spyReporter{}
	reviewer := &Reviewer{
		Agent:  NewAgent(lm, registry, WithReporter(spy)),
		Config: cfg,
	}
	registry.SetVerdictChangeListener(func(ctx context.Context, verdict string, rationale string) error {
		reviewer.SetVerdict(ReviewVerdict(verdict))
		return nil
	})

	reviewer.SetVerdict(VerdictRejected)
	require.Equal(t, VerdictRejected, reviewer.GetVerdict())

	reviewer.Agent.history = []llm.Message{
		{Role: llm.RoleSystem, Text: "system prompt"},
		{Role: llm.RoleAssistant, Text: "automated review with ❌ Rejected"},
	}

	err = reviewer.executePostReviewTurn(context.Background(), "The edge case is handled upstream in caller.go:10", 5, nil)
	require.NoError(t, err)
	require.Equal(t, VerdictApproved, reviewer.GetVerdict())
}
