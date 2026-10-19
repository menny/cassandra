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

		r := NewRegistry()
		registerT3Tools(r)

		require.Empty(t, r.ToTools())
	})

	t.Run("inside T3 environment", func(t *testing.T) {
		t.Setenv("T3_ACP_MCP_ENDPOINT", "http://127.0.0.1:3773/mcp")

		r := NewRegistry()
		registerT3Tools(r)

		toolDefs := r.ToTools()
		require.Len(t, toolDefs, 2)

		var hasLink, hasList bool
		for _, td := range toolDefs {
			if td.Name == "link_pull_request" {
				hasLink = true
			} else if td.Name == "list_thread_pull_requests" {
				hasList = true
			}
		}
		require.True(t, hasLink)
		require.True(t, hasList)

		tc := llm.ToolCall{
			ID:        "tc_1",
			Name:      "link_pull_request",
			Arguments: `{"url": "https://github.com/owner/repo/pull/123"}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, "alreadyLinked")
	})
}
