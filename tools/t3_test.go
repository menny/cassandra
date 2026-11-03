package tools

import (
	"context"
	"encoding/json"
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
        echo '{"inheritedProviderInstanceId":"antigravity","inheritedModel":"gemini-3.8-flash-high","providers":[{"providerInstanceId":"antigravity","canRunChildTask":true,"models":[{"id":"gemini-3.8-flash-low"}]}]}'
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

const mockCapabilitiesJSON = `{
  "parentThreadId": "f672b55e-f4a6-405d-b333-cc5bd8c5766f",
  "inheritedProviderInstanceId": "antigravity",
  "inheritedModel": "gemini-3.8-flash-high",
  "providers": [
    {
      "providerInstanceId": "codex",
      "displayName": "Codex",
      "canRunChildTask": false,
      "constraints": ["Provider instance is disabled."],
      "models": []
    },
    {
      "providerInstanceId": "opencode",
      "displayName": "OpenCode",
      "canRunChildTask": true,
      "constraints": [],
      "models": [
        {"id": "deepseek-chat", "label": "DeepSeek Chat"},
        {"id": "gemini-3.8-flash", "label": "Gemini 3.8 Flash"}
      ]
    },
    {
      "providerInstanceId": "antigravity",
      "displayName": "Antigravity",
      "canRunChildTask": true,
      "constraints": [],
      "models": [
        {"id": "gemini-4-argon-pro", "label": "Gemini 4 Argon Pro"},
        {"id": "gemini-3.8-flash-high", "label": "Gemini 3.8 Flash High"}
      ]
    },
    {
      "providerInstanceId": "cassandra",
      "displayName": "Cassandra",
      "driverKind": "acpRegistry",
      "canRunChildTask": true,
      "constraints": [],
      "models": [
        {"id": "gemini-3.8-flash", "label": "Gemini 3.8 Flash"}
      ]
    }
  ]
}`

func TestMaskCassandraInCapabilities(t *testing.T) {
	masked := MaskCassandraInCapabilities(mockCapabilitiesJSON)
	require.Contains(t, masked, "Cassandra is a read-only code review agent")

	var caps OrchestratorCapabilities
	require.NoError(t, json.Unmarshal([]byte(masked), &caps))
	for _, p := range caps.Providers {
		if p.ProviderInstanceID == "cassandra" {
			require.False(t, p.CanRunChildTask)
			require.Contains(t, p.Constraints, "Cassandra is a read-only code review agent and cannot execute code fixes.")
		}
	}
}

func TestFilterEnabledCodingProviders(t *testing.T) {
	var caps OrchestratorCapabilities
	require.NoError(t, json.Unmarshal([]byte(mockCapabilitiesJSON), &caps))

	enabled := FilterEnabledCodingProviders(caps.Providers)
	require.Len(t, enabled, 2)
	ids := []string{enabled[0].ProviderInstanceID, enabled[1].ProviderInstanceID}
	require.Contains(t, ids, "opencode")
	require.Contains(t, ids, "antigravity")
	require.NotContains(t, ids, "cassandra")
	require.NotContains(t, ids, "codex")
}

func TestResolveLaunchHarness(t *testing.T) {
	t.Run("default priority selects antigravity", func(t *testing.T) {
		cfg := T3LaunchConfig{
			PreferredHarnesses: []string{"antigravity", "opencode"},
		}
		sel, err := ResolveLaunchHarness(mockCapabilitiesJSON, cfg, nil)
		require.NoError(t, err)
		require.Equal(t, "antigravity", sel.InstanceID)
		require.Equal(t, "gemini-3.8-flash-high", sel.Model) // inheritedModel matches
	})

	t.Run("custom priority selects opencode", func(t *testing.T) {
		cfg := T3LaunchConfig{
			PreferredHarnesses: []string{"opencode", "antigravity"},
		}
		sel, err := ResolveLaunchHarness(mockCapabilitiesJSON, cfg, nil)
		require.NoError(t, err)
		require.Equal(t, "opencode", sel.InstanceID)
		require.Equal(t, "deepseek-chat", sel.Model) // first model in opencode
	})

	t.Run("preferred model from toml is honored", func(t *testing.T) {
		cfg := T3LaunchConfig{
			PreferredHarnesses: []string{"opencode", "antigravity"},
			PreferredModel:     "gemini-3.8-flash",
		}
		sel, err := ResolveLaunchHarness(mockCapabilitiesJSON, cfg, nil)
		require.NoError(t, err)
		require.Equal(t, "opencode", sel.InstanceID)
		require.Equal(t, "gemini-3.8-flash", sel.Model)
	})

	t.Run("ignores cassandra when requested and applies fallback", func(t *testing.T) {
		cfg := T3LaunchConfig{
			PreferredHarnesses: []string{"antigravity"},
		}
		req := &ModelSelection{
			InstanceID: "cassandra",
			Model:      "gemini-3.8-flash",
		}
		sel, err := ResolveLaunchHarness(mockCapabilitiesJSON, cfg, req)
		require.NoError(t, err)
		require.Equal(t, "antigravity", sel.InstanceID)
	})

	t.Run("honors requested valid coding harness", func(t *testing.T) {
		cfg := T3LaunchConfig{
			PreferredHarnesses: []string{"antigravity"},
		}
		req := &ModelSelection{
			InstanceID: "opencode",
			Model:      "deepseek-chat",
		}
		sel, err := ResolveLaunchHarness(mockCapabilitiesJSON, cfg, req)
		require.NoError(t, err)
		require.Equal(t, "opencode", sel.InstanceID)
		require.Equal(t, "deepseek-chat", sel.Model)
	})

	t.Run("fallback when top preferred harness is disabled", func(t *testing.T) {
		cfg := T3LaunchConfig{
			PreferredHarnesses: []string{"claudeAgent", "codex", "opencode"},
		}
		sel, err := ResolveLaunchHarness(mockCapabilitiesJSON, cfg, nil)
		require.NoError(t, err)
		require.Equal(t, "opencode", sel.InstanceID)
	})
}

func TestGetAvailableCodingHarnessChoices(t *testing.T) {
	cfg := T3LaunchConfig{
		PreferredHarnesses: []string{"antigravity", "opencode"},
		PreferredModel:     "gemini-3.8-flash-high",
	}
	choices := GetAvailableCodingHarnessChoices(mockCapabilitiesJSON, cfg)
	require.Len(t, choices, 2)

	options := FormatHarnessOptionsForDeveloper(choices)
	require.Len(t, options, 2)
	require.Contains(t, options[1], "(Recommended) Antigravity — Gemini 3.8 Flash High")
	require.Contains(t, options[0], "OpenCode — DeepSeek Chat")
}

func TestT3ThreadLaunch_DefensiveCassandraResolution(t *testing.T) {
	tmpDir := t.TempDir()
	mockScript := filepath.Join(tmpDir, "mock_t3_node.sh")
	scriptContent := `#!/bin/sh
for arg in "$@"; do
    case "$prev" in
        "acp-mcp-call") tool="$arg" ;;
    esac
    prev="$arg"
done

case "$tool" in
    "orchestrator_capabilities")
        echo '` + mockCapabilitiesJSON + `'
        ;;
    "t3_thread_launch")
        echo "$*" >> ` + filepath.Join(tmpDir, "launch.log") + `
        echo '{"threadId":"spawned-fix-thread-456"}'
        ;;
    *)
        exit 1
        ;;
esac
`
	require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))
	t.Setenv("T3_ACP_MCP_NODE", mockScript)

	r := NewRegistry()
	r.SetT3Config(T3LaunchConfig{
		PreferredHarnesses: []string{"antigravity", "opencode"},
	})
	registerT3Tools(r)

	// Simulate LLM attempting to pass cassandra as the instanceId
	tc := llm.ToolCall{
		ID:   "call_fix",
		Name: "t3_thread_launch",
		Arguments: `{
			"title": "Fix nil dereference in auth",
			"message": "Please fix the nil dereference",
			"modelSelection": {
				"instanceId": "cassandra",
				"model": "gemini-3.8-flash"
			}
		}`,
	}

	res, err := r.HandleCall(context.Background(), tc)
	require.NoError(t, err)
	require.Contains(t, res, "spawned-fix-thread-456")

	// Verify that the launch call was intercepted and rewritten with antigravity!
	launchLog, err := os.ReadFile(filepath.Join(tmpDir, "launch.log"))
	require.NoError(t, err)
	require.NotContains(t, string(launchLog), `"instanceId":"cassandra"`)
	require.Contains(t, string(launchLog), `"instanceId":"antigravity"`)
}

func TestGetAvailableCodingHarnessChoices_WhenFirstPreferenceDisabled(t *testing.T) {
	cfg := T3LaunchConfig{
		PreferredHarnesses: []string{"codex", "opencode"}, // codex is disabled in mockCapabilitiesJSON
	}
	choices := GetAvailableCodingHarnessChoices(mockCapabilitiesJSON, cfg)
	require.Len(t, choices, 2)

	options := FormatHarnessOptionsForDeveloper(choices)
	require.Len(t, options, 2)
	require.Contains(t, options[0], "(Recommended) OpenCode")
}

type testPrompter struct {
	response AskDeveloperResponse
	called   bool
}

func (p *testPrompter) AskDeveloper(ctx context.Context, req AskDeveloperRequest) (AskDeveloperResponse, error) {
	p.called = true
	return p.response, nil
}

func TestT3ThreadLaunch_InteractivePrompt(t *testing.T) {
	tmpDir := t.TempDir()
	mockScript := filepath.Join(tmpDir, "mock_t3_node.sh")
	scriptContent := `#!/bin/sh
for arg in "$@"; do
    case "$prev" in
        "acp-mcp-call") tool="$arg" ;;
    esac
    prev="$arg"
done

case "$tool" in
    "orchestrator_capabilities")
        echo '` + mockCapabilitiesJSON + `'
        ;;
    "t3_thread_launch")
        echo "$*" >> ` + filepath.Join(tmpDir, "launch.log") + `
        echo '{"threadId":"spawned-fix-thread-789"}'
        ;;
    *)
        exit 1
        ;;
esac
`
	require.NoError(t, os.WriteFile(mockScript, []byte(scriptContent), 0o755))
	t.Setenv("T3_ACP_MCP_NODE", mockScript)

	t.Run("prompts developer and applies user selection", func(t *testing.T) {
		r := NewRegistry()
		r.SetT3Config(T3LaunchConfig{
			PreferredHarnesses: []string{"antigravity", "opencode"},
			PromptForHarness:   true,
		})
		prompter := &testPrompter{
			response: AskDeveloperResponse{
				Status:   "answered",
				Selected: []string{"OpenCode — DeepSeek Chat"},
			},
		}
		r.SetDeveloperPrompter(prompter)
		registerT3Tools(r)

		tc := llm.ToolCall{
			ID:        "call_fix_prompt",
			Name:      "t3_thread_launch",
			Arguments: `{"title": "Review Fix", "message": "fix it"}`,
		}

		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, "spawned-fix-thread-789")
		require.True(t, prompter.called)

		launchLog, err := os.ReadFile(filepath.Join(tmpDir, "launch.log"))
		require.NoError(t, err)
		require.Contains(t, string(launchLog), `"instanceId":"opencode"`)
	})

	t.Run("handles capabilities query failure", func(t *testing.T) {
		failScript := filepath.Join(tmpDir, "mock_fail_node.sh")
		fContent := `#!/bin/sh
exit 1
`
		require.NoError(t, os.WriteFile(failScript, []byte(fContent), 0o755))
		t.Setenv("T3_ACP_MCP_NODE", failScript)

		r := NewRegistry()
		registerT3Tools(r)

		tc := llm.ToolCall{
			ID:        "call_fix_fail",
			Name:      "t3_thread_launch",
			Arguments: `{"title": "Review Fix", "message": "fix it"}`,
		}

		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to query orchestrator capabilities")
	})

	t.Run("returns error when no enabled coding harnesses exist", func(t *testing.T) {
		onlyCassandraScript := filepath.Join(tmpDir, "mock_only_cassandra.sh")
		cContent := `#!/bin/sh
echo '{"providers":[{"providerInstanceId":"cassandra","canRunChildTask":true,"constraints":[]}]}'
`
		require.NoError(t, os.WriteFile(onlyCassandraScript, []byte(cContent), 0o755))
		t.Setenv("T3_ACP_MCP_NODE", onlyCassandraScript)

		r := NewRegistry()
		registerT3Tools(r)

		tc := llm.ToolCall{
			ID:        "call_no_coding_harness",
			Name:      "t3_thread_launch",
			Arguments: `{"title": "Review Fix", "message": "fix it"}`,
		}

		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no enabled coding agent harnesses available")
	})
}
