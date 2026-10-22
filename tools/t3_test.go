package tools

import (
	"context"
	"os"
	"path/filepath"
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

		// Directly calling callT3McpTool outside T3 returns an error
		_, err := callT3McpTool(context.Background(), "orchestrator_capabilities", map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "only available in a T3 Code session")
	})

	t.Run("inside T3 environment without node binary", func(t *testing.T) {
		t.Setenv("T3_ACP_MCP_ENDPOINT", "http://127.0.0.1:3773/mcp")
		t.Setenv("T3_ACP_MCP_NODE", "")

		require.False(t, IsT3Session())

		r := NewRegistry()
		registerT3Tools(r)

		require.Empty(t, r.ToTools())
	})

	t.Run("inside T3 environment with mock node binary", func(t *testing.T) {
		tmpDir := t.TempDir()
		mockScript := filepath.Join(tmpDir, "mock_node.sh")
		scriptContent := `#!/bin/sh
# Arguments: [entrypoint] acp-mcp-call <tool> <argsJSON>
for arg in "$@"; do
    case "$prev" in
        "acp-mcp-call") tool="$arg" ;;
    esac
    prev="$arg"
done

case "$tool" in
    "link_pull_request")
        echo '{"alreadyLinked":true,"success":true}'
        ;;
    "list_thread_pull_requests")
        echo '{"pullRequests":[]}'
        ;;
    "orchestrator_capabilities")
        echo '{"inheritedProviderInstanceId":"antigravity","inheritedModel":"gemini-3.8-flash-high","providers":[{"providerInstanceId":"antigravity","models":[{"id":"gemini-3.8-flash-low"}]}]}'
        ;;
    "t3_thread_launch")
        echo '{"threadId":"test-thread-id-123"}'
        ;;
    "t3_thread_list")
        echo '{"projectId":"p1","currentThreadId":"t1","threads":[{"threadId":"t1","title":"Review Feature A"}]}'
        ;;
    "t3_thread_update")
        echo '{"success":true}'
        ;;
    *)
        exit 1
        ;;
esac
`
		require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))

		t.Setenv("T3_ACP_MCP_ENDPOINT", "http://127.0.0.1:3773/mcp")
		t.Setenv("T3_ACP_MCP_NODE", mockScript)

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

	t.Run("truncates output exceeding 40 KB", func(t *testing.T) {
		tmpDir := t.TempDir()
		mockScript := filepath.Join(tmpDir, "mock_node.sh")
		scriptContent := `#!/bin/sh
printf '%*s' 50000 | tr ' ' 'A'
`
		require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))

		t.Setenv("T3_ACP_MCP_NODE", mockScript)

		res, err := callT3McpTool(context.Background(), "orchestrator_capabilities", map[string]any{})
		require.NoError(t, err)
		require.True(t, len(res) <= maxT3ToolOutputBytes+len("\n... (truncated)"))
		require.Contains(t, res, "... (truncated)")
	})
}

func TestCleanAndFormatThreadTitle(t *testing.T) {
	t.Run("CleanThreadTitle", func(t *testing.T) {
		require.Equal(t, "Review my feature", CleanThreadTitle("Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("⏳ Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("✅ Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("❎ Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("✳️ Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("❌ Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("💬 Review my feature"))
		require.Equal(t, "Review my feature", CleanThreadTitle("⏳ ✅ Review my feature"))
		require.Equal(t, "", CleanThreadTitle("⏳ "))
	})

	t.Run("FormatThreadTitle", func(t *testing.T) {
		require.Equal(t, "⏳ Review my feature", FormatThreadTitle(ReviewStatePending, "Review my feature"))
		require.Equal(t, "✅ Review my feature", FormatThreadTitle(ReviewStateApproved, "⏳ Review my feature"))
		require.Equal(t, "❎ Review my feature", FormatThreadTitle(ReviewStateRejected, "⏳ Review my feature"))
		require.Equal(t, "✳️ Review my feature", FormatThreadTitle(ReviewStateElse, "⏳ Review my feature"))
		require.Equal(t, "✅ Review", FormatThreadTitle(ReviewStateApproved, ""))
	})
}

func TestThreadTitlePrefixUpdates(t *testing.T) {
	t.Run("outside T3 session is no-op", func(t *testing.T) {
		t.Setenv("T3_ACP_MCP_NODE", "")
		err := UpdateThreadTitlePrefix(context.Background(), ReviewStatePending)
		require.NoError(t, err)

		_, err = GetCurrentThreadTitle(context.Background())
		require.Error(t, err)
	})

	t.Run("supports threads with id key instead of threadId", func(t *testing.T) {
		tmpDir := t.TempDir()
		mockScript := filepath.Join(tmpDir, "mock_node_id.sh")
		scriptContent := `#!/bin/sh
for arg in "$@"; do
    case "$prev" in
        "acp-mcp-call") tool="$arg" ;;
    esac
    prev="$arg"
done

case "$tool" in
    "t3_thread_list")
        echo '{"currentThreadId":"t2","threads":[{"id":"t1","title":"Other"},{"id":"t2","title":"⏳ Review Thread 2"}]}'
        ;;
    *)
        exit 1
        ;;
esac
`
		require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))
		t.Setenv("T3_ACP_MCP_NODE", mockScript)

		title, err := GetCurrentThreadTitle(context.Background())
		require.NoError(t, err)
		require.Equal(t, "⏳ Review Thread 2", title)
	})

	t.Run("inside T3 session updates title", func(t *testing.T) {
		tmpDir := t.TempDir()
		mockScript := filepath.Join(tmpDir, "mock_node.sh")
		scriptContent := `#!/bin/sh
for arg in "$@"; do
    case "$prev" in
        "acp-mcp-call") tool="$arg" ;;
    esac
    prev="$arg"
done

case "$tool" in
    "t3_thread_list")
        echo '{"projectId":"p1","currentThreadId":"t1","threads":[{"threadId":"t1","title":"⏳ Review Feature A"}]}'
        ;;
    "t3_thread_update")
        echo "$*" >> ` + filepath.Join(tmpDir, "updated.log") + `
        echo '{"success":true}'
        ;;
    *)
        exit 1
        ;;
esac
`
		require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))
		t.Setenv("T3_ACP_MCP_NODE", mockScript)

		title, err := GetCurrentThreadTitle(context.Background())
		require.NoError(t, err)
		require.Equal(t, "⏳ Review Feature A", title)

		err = UpdateThreadTitlePrefix(context.Background(), ReviewStateApproved)
		require.NoError(t, err)

		logContent, err := os.ReadFile(filepath.Join(tmpDir, "updated.log"))
		require.NoError(t, err)
		require.Contains(t, string(logContent), "✅ Review Feature A")
	})

	t.Run("handles wrapped MCP tool result envelope", func(t *testing.T) {
		tmpDir := t.TempDir()
		mockScript := filepath.Join(tmpDir, "mock_mcp_node.sh")
		scriptContent := `#!/bin/sh
for arg in "$@"; do
    case "$prev" in
        "acp-mcp-call") tool="$arg" ;;
    esac
    prev="$arg"
done

case "$tool" in
    "t3_thread_list")
        echo '{"content":[{"type":"text","text":"..."}],"structuredContent":{"projectId":"p1","currentThreadId":"t1","threads":[{"threadId":"t1","title":"⏳ Review Reduce LunaServer"}]},"isError":false}'
        ;;
    "t3_thread_update")
        echo "$*" >> ` + filepath.Join(tmpDir, "updated.log") + `
        echo '{"content":[{"type":"text","text":"done"}],"structuredContent":{"title":"✳️ Review Reduce LunaServer"},"isError":false}'
        ;;
    *)
        exit 1
        ;;
esac
`
		require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))
		t.Setenv("T3_ACP_MCP_NODE", mockScript)

		title, err := GetCurrentThreadTitle(context.Background())
		require.NoError(t, err)
		require.Equal(t, "⏳ Review Reduce LunaServer", title)

		err = UpdateThreadTitlePrefix(context.Background(), ReviewStateElse)
		require.NoError(t, err)

		logContent, err := os.ReadFile(filepath.Join(tmpDir, "updated.log"))
		require.NoError(t, err)
		require.Contains(t, string(logContent), "✳️ Review Reduce LunaServer")
	})

	t.Run("handles MCP error envelope properly", func(t *testing.T) {
		tmpDir := t.TempDir()
		mockScript := filepath.Join(tmpDir, "mock_error_node.sh")
		scriptContent := `#!/bin/sh
echo '{"content":[{"type":"text","text":"rate limit exceeded"}],"isError":true}'
`
		require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))
		t.Setenv("T3_ACP_MCP_NODE", mockScript)

		_, err := callT3McpTool(context.Background(), "t3_thread_list", map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "rate limit exceeded")
	})
}
