package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/menny/cassandra/llm"
)

func TestAgent_Metrics(t *testing.T) {
	t.Run("collects metrics through ReAct loop", func(t *testing.T) {
		lm := &mockLLM{responses: []*llm.Response{
			{
				ToolCalls: []llm.ToolCall{
					makeToolCall("tc1", "read_file", map[string]any{"file_path": "a.go"}),
				},
				Usage: llm.Usage{PromptTokens: 100, OutputTokens: 50, CachedTokens: 200},
			},
			{
				ToolCalls: []llm.ToolCall{
					makeToolCall("tc2", "read_file", map[string]any{"file_path": "b.go"}),
					makeToolCall("tc3", "glob_files", map[string]any{"pattern": "*.go"}),
				},
				Usage: llm.Usage{PromptTokens: 150, OutputTokens: 75, ThinkingTokens: 25},
			},
			{
				Text:  "final review",
				Usage: llm.Usage{PromptTokens: 200, OutputTokens: 100},
			},
		}}

		d := newMockDispatcher()
		d.handlers["read_file"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "content", nil }
		d.handlers["glob_files"] = func(ctx context.Context, _ llm.ToolCall) (string, error) { return "files", nil }

		agent := newTestAgent(lm, d)
		_, err := agent.RunReview(context.Background(), "sys", "", "req", 5, 1024)
		require.NoError(t, err)

		metrics := agent.GetMetrics()

		// 3 iterations in total (2 tool turns + 1 final text turn)
		assert.Equal(t, 3, metrics.Iterations)

		// Tool calls: 2x read_file, 1x glob_files
		assert.Equal(t, 3, metrics.ToolCalls.Total)
		assert.Equal(t, 2, metrics.ToolCalls.ByTool["read_file"])
		assert.Equal(t, 1, metrics.ToolCalls.ByTool["glob_files"])

		// Tokens breakdown:
		// Input: 100 + 150 + 200 = 450
		// Output: 50 + 75 + 100 = 225
		// Thinking: 0 + 25 + 0 = 25
		// Cached: 200 + 0 + 0 = 200
		assert.Equal(t, 450, metrics.Tokens.Input)
		assert.Equal(t, 225, metrics.Tokens.Output)
		assert.Equal(t, 25, metrics.Tokens.Thinking)
		assert.Equal(t, 200, metrics.Tokens.Cached)
		assert.Equal(t, 650, metrics.Tokens.TotalInput)  // 450 + 200
		assert.Equal(t, 250, metrics.Tokens.TotalOutput) // 225 + 25
	})

	t.Run("includes extraction pass tokens", func(t *testing.T) {
		lm := &mockLLM{responses: []*llm.Response{
			{
				Text:  "raw review",
				Usage: llm.Usage{PromptTokens: 100, OutputTokens: 50},
			},
			{
				Text:  `{"approval":{"approved":true,"rationale":"ok","action":"APPROVE"},"files_review":[]}`,
				Usage: llm.Usage{PromptTokens: 200, OutputTokens: 150},
			},
		}}

		agent := newTestAgent(lm, newMockDispatcher())
		_, err := agent.RunReview(context.Background(), "sys", "", "req", 5, 1024)
		require.NoError(t, err)

		_, err = agent.ExtractStructuredReview(context.Background(), "extraction sys", "raw review", llm.StructuredConfig{})
		require.NoError(t, err)

		metrics := agent.GetMetrics()

		// 1 iteration for RunReview (direct answer)
		assert.Equal(t, 1, metrics.Iterations)

		// Tokens (Review + Extraction):
		// Input: 100 + 200 = 300
		// Output: 50 + 150 = 200
		assert.Equal(t, 300, metrics.Tokens.Input)
		assert.Equal(t, 200, metrics.Tokens.Output)
	})
}
