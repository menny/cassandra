package tools

import (
	"context"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestRegisterT3Tools(t *testing.T) {
	t.Run("outside T3 environment", func(t *testing.T) {
		t.Setenv("T3_ACP_MCP_ENDPOINT", "")
		t.Setenv("T3_ACP_MCP_NODE", "")

		require.False(t, IsT3Session())

		r := NewRegistry()
		registerT3Tools(r)

		require.Empty(t, r.ToTools())
	})

	t.Run("inside T3 environment", func(t *testing.T) {
		t.Setenv("T3_ACP_MCP_ENDPOINT", "http://127.0.0.1:3773/mcp")

		require.True(t, IsT3Session())

		r := NewRegistry()
		registerT3Tools(r)

		toolDefs := r.ToTools()
		require.Len(t, toolDefs, 4)

		toolMap := make(map[string]bool)
		for _, td := range toolDefs {
			toolMap[td.Name] = true
		}
		require.True(t, toolMap["link_pull_request"])
		require.True(t, toolMap["list_thread_pull_requests"])
		require.True(t, toolMap["orchestrator_capabilities"])
		require.True(t, toolMap["t3_thread_launch"])

		// Test link_pull_request invocation
		tcLink := llm.ToolCall{
			ID:        "tc_link",
			Name:      "link_pull_request",
			Arguments: `{"url": "https://github.com/owner/repo/pull/123"}`,
		}
		resLink, err := r.HandleCall(context.Background(), tcLink)
		require.NoError(t, err)
		require.Contains(t, resLink, "alreadyLinked")

		// Test orchestrator_capabilities invocation
		tcCaps := llm.ToolCall{
			ID:        "tc_caps",
			Name:      "orchestrator_capabilities",
			Arguments: `{}`,
		}
		resCaps, err := r.HandleCall(context.Background(), tcCaps)
		require.NoError(t, err)
		require.Contains(t, resCaps, "providers")
		require.Contains(t, resCaps, "inheritedProviderInstanceId")

		// Test t3_thread_launch invocation with title prefix formatting
		tcLaunch := llm.ToolCall{
			ID:   "tc_launch",
			Name: "t3_thread_launch",
			Arguments: `{
				"title": "Fix memory leak in buffer pool",
				"message": "Please fix the leak in buffer pool",
				"modelSelection": {
					"instanceId": "antigravity",
					"model": "gemini-3.8-flash-low"
				}
			}`,
		}
		resLaunch, err := r.HandleCall(context.Background(), tcLaunch)
		require.NoError(t, err)
		require.Contains(t, resLaunch, "threadId")
	})
}
