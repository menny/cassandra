package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/menny/cassandra/llm"
)

func TestIsOmnigentSession(t *testing.T) {
	t.Run("outside Omnigent environment", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "")
		t.Setenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID", "")
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")
		t.Setenv("OMNIGENT_BRIDGE_DIR", "")
		t.Setenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR", "")

		require.False(t, IsOmnigentSession())
	})

	t.Run("with OMNIGENT=1", func(t *testing.T) {
		t.Setenv("OMNIGENT", "1")
		require.True(t, IsOmnigentSession())
	})

	t.Run("with OMNIGENT_RUNNER_WORKSPACE", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "/path/to/workspace")
		require.True(t, IsOmnigentSession())
	})

	t.Run("with OMNIGENT_RELAY_URL", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "")
		t.Setenv("OMNIGENT_RELAY_URL", "http://127.0.0.1:28700")
		require.True(t, IsOmnigentSession())
	})

	t.Run("with OMNIGENT_BRIDGE_DIR", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "")
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_BRIDGE_DIR", "/path/to/bridge")
		require.True(t, IsOmnigentSession())
	})
}

func TestResolveOmnigentRelay(t *testing.T) {
	t.Run("from environment variables", func(t *testing.T) {
		t.Setenv("OMNIGENT_RELAY_URL", "http://127.0.0.1:12345")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "secret-token")
		t.Setenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID", "sess-123")

		cfg, err := resolveOmnigentRelay()
		require.NoError(t, err)
		require.Equal(t, "http://127.0.0.1:12345", cfg.URL)
		require.Equal(t, "secret-token", cfg.Token)
		require.Equal(t, "sess-123", cfg.SessionID)
	})

	t.Run("from bridge dir tool_relay.json", func(t *testing.T) {
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")

		tmpDir := t.TempDir()
		relayData := map[string]string{
			"url":        "http://127.0.0.1:23456",
			"token":      "bridge-token",
			"session_id": "bridge-sess",
		}
		raw, err := json.Marshal(relayData)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "tool_relay.json"), raw, 0o600))

		t.Setenv("OMNIGENT_BRIDGE_DIR", tmpDir)

		cfg, err := resolveOmnigentRelay()
		require.NoError(t, err)
		require.Equal(t, "http://127.0.0.1:23456", cfg.URL)
		require.Equal(t, "bridge-token", cfg.Token)
		require.Equal(t, "bridge-sess", cfg.SessionID)
	})

	t.Run("from bridge dir tool_relay.env", func(t *testing.T) {
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")

		tmpDir := t.TempDir()
		envContent := "OMNIGENT_RELAY_URL='http://127.0.0.1:34567'\nOMNIGENT_RELAY_TOKEN='env-token'\n"
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "tool_relay.env"), []byte(envContent), 0o600))

		t.Setenv("OMNIGENT_BRIDGE_DIR", tmpDir)

		cfg, err := resolveOmnigentRelay()
		require.NoError(t, err)
		require.Equal(t, "http://127.0.0.1:34567", cfg.URL)
		require.Equal(t, "env-token", cfg.Token)
	})

	t.Run("unreachable relay returns error", func(t *testing.T) {
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")
		t.Setenv("OMNIGENT_BRIDGE_DIR", t.TempDir()) // empty dir
		t.Setenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR", "")
		t.Setenv("HOME", t.TempDir()) // empty home

		_, err := resolveOmnigentRelay()
		require.Error(t, err)
		require.Contains(t, err.Error(), "omnigent tool relay is not configured or reachable")
	})
}

func TestCallOmnigentTool(t *testing.T) {
	t.Run("outside Omnigent session returns error", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "")
		t.Setenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID", "")
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")
		t.Setenv("OMNIGENT_BRIDGE_DIR", "")
		t.Setenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR", "")

		_, err := callOmnigentTool(context.Background(), "sys_session_get_info", map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "only available in an Omnigent session")
	})

	t.Run("successful call with standard MCP content envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/tool", r.URL.Path)
			require.Equal(t, "Bearer test-secret-token", r.Header.Get("Authorization"))
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var payload struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "sys_session_get_info", payload.Name)

			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `{"title": "Review Auth Refactor"}`},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-secret-token")

		res, err := callOmnigentTool(context.Background(), "sys_session_get_info", map[string]any{})
		require.NoError(t, err)
		require.Equal(t, `{"title": "Review Auth Refactor"}`, res)
	})

	t.Run("successful call with structuredContent", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"structuredContent": map[string]any{
					"conversation_id": "conv-456",
					"status":          "running",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-secret-token")

		res, err := callOmnigentTool(context.Background(), "sys_session_create", map[string]any{})
		require.NoError(t, err)
		require.Contains(t, res, "conv-456")
	})

	t.Run("MCP error envelope returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"isError": true,
				"content": []map[string]any{
					{"type": "text", "text": "agent not found"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-secret-token")

		_, err := callOmnigentTool(context.Background(), "sys_session_create", map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "agent not found")
	})

	t.Run("HTTP error status returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("gateway failure"))
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-secret-token")

		_, err := callOmnigentTool(context.Background(), "sys_session_get_info", map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "HTTP 502")
	})

	t.Run("truncation of excessively long responses", func(t *testing.T) {
		largeText := strings.Repeat("A", maxOmnigentToolOutputBytes+1000)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": largeText},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-secret-token")

		res, err := callOmnigentTool(context.Background(), "sys_agent_list", map[string]any{})
		require.NoError(t, err)
		require.True(t, len(res) <= maxOmnigentToolOutputBytes+len("\n... (truncated)"))
		require.True(t, strings.HasSuffix(res, "... (truncated)"))
	})

	t.Run("context cancellation terminates request immediately", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
				_, _ = w.Write([]byte(`{}`))
			}
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-secret-token")

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		_, err := callOmnigentTool(ctx, "sys_session_get_info", map[string]any{})
		require.Error(t, err)
	})
}

func sampleOmnigentAgents() string {
	bTrue := true
	bFalse := false
	resp := OmnigentAgentListResponse{
		Builtins: []OmnigentAgentEntry{
			{
				AgentID:         "ag-1",
				Name:            "antigravity-native-ui",
				Harness:         "antigravity-native",
				AvailableOnHost: &bTrue,
			},
			{
				AgentID:         "ag-2",
				Name:            "opencode-native-ui",
				Harness:         "opencode-native",
				AvailableOnHost: &bTrue,
			},
			{
				AgentID:           "ag-3",
				Name:              "claude-native-ui",
				Harness:           "claude-native",
				AvailableOnHost:   &bFalse,
				UnavailableReason: "needs-auth",
			},
			{
				AgentID:         "ag-cassandra",
				Name:            "cassandra",
				Harness:         "acp:cassandra",
				AvailableOnHost: &bTrue,
			},
		},
	}
	raw, _ := json.Marshal(resp)
	return string(raw)
}

func TestMaskCassandraInOmnigentAgents(t *testing.T) {
	raw := sampleOmnigentAgents()
	masked := MaskCassandraInOmnigentAgents(raw)

	var resp OmnigentAgentListResponse
	require.NoError(t, json.Unmarshal([]byte(masked), &resp))

	for _, a := range resp.Builtins {
		require.False(t, strings.EqualFold(a.Name, "cassandra"))
		require.False(t, strings.Contains(strings.ToLower(a.Harness), "cassandra"))
	}
}

func TestFilterEnabledOmnigentAgents(t *testing.T) {
	raw := sampleOmnigentAgents()
	var resp OmnigentAgentListResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	enabled := FilterEnabledOmnigentAgents(resp.Builtins)
	require.Len(t, enabled, 2)
	require.Equal(t, "antigravity-native-ui", enabled[0].Name)
	require.Equal(t, "opencode-native-ui", enabled[1].Name)
}

func TestResolveOmnigentLaunchAgent(t *testing.T) {
	raw := sampleOmnigentAgents()

	t.Run("honors requested valid agent", func(t *testing.T) {
		cfg := OmnigentLaunchConfig{
			PreferredHarnesses: []string{"antigravity-native-ui"},
		}
		a, err := ResolveOmnigentLaunchAgent(raw, cfg, "ag-2")
		require.NoError(t, err)
		require.Equal(t, "opencode-native-ui", a.Name)
	})

	t.Run("defensively rejects Cassandra even if requested", func(t *testing.T) {
		cfg := OmnigentLaunchConfig{
			PreferredHarnesses: []string{"antigravity-native-ui"},
		}
		a, err := ResolveOmnigentLaunchAgent(raw, cfg, "ag-cassandra")
		require.NoError(t, err)
		// Should fall back to preferred harness instead of Cassandra
		require.Equal(t, "antigravity-native-ui", a.Name)
	})

	t.Run("matches preferred harnesses by substring / prefix", func(t *testing.T) {
		cfg := OmnigentLaunchConfig{
			PreferredHarnesses: []string{"opencode", "antigravity"},
		}
		a, err := ResolveOmnigentLaunchAgent(raw, cfg, "")
		require.NoError(t, err)
		require.Equal(t, "opencode-native-ui", a.Name)
	})

	t.Run("fallback to first enabled agent when preferred not available", func(t *testing.T) {
		cfg := OmnigentLaunchConfig{
			PreferredHarnesses: []string{"non-existent-agent"},
		}
		a, err := ResolveOmnigentLaunchAgent(raw, cfg, "")
		require.NoError(t, err)
		require.Equal(t, "antigravity-native-ui", a.Name)
	})

	t.Run("returns error when no agents available", func(t *testing.T) {
		emptyResp, _ := json.Marshal(OmnigentAgentListResponse{})
		cfg := OmnigentLaunchConfig{}
		_, err := ResolveOmnigentLaunchAgent(string(emptyResp), cfg, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "no enabled coding agent harnesses")
	})
}

func TestGetAvailableOmnigentAgentChoices(t *testing.T) {
	raw := sampleOmnigentAgents()
	cfg := OmnigentLaunchConfig{
		PreferredHarnesses: []string{"antigravity"},
	}

	choices := GetAvailableOmnigentAgentChoices(raw, cfg)
	require.Len(t, choices, 2)
	require.True(t, choices[0].IsPreferred)
	require.False(t, choices[1].IsPreferred)

	formatted := FormatOmnigentAgentOptionsForDeveloper(choices)
	require.Len(t, formatted, 2)
	require.True(t, strings.HasPrefix(formatted[0], "(Recommended) "))
	require.False(t, strings.HasPrefix(formatted[1], "(Recommended) "))
}

func TestRegisterOmnigentTools(t *testing.T) {
	t.Run("outside Omnigent environment does not register tools", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "")
		t.Setenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID", "")
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")
		t.Setenv("OMNIGENT_BRIDGE_DIR", "")
		t.Setenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR", "")

		r := NewRegistry()
		registerOmnigentTools(r)
		require.Empty(t, r.ToTools())
	})

	t.Run("inside Omnigent environment registers capabilities and launch tools", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)

			if payload.Name == "sys_agent_list" {
				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": sampleOmnigentAgents()},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			} else if payload.Name == "sys_session_create" {
				// Assert placement defaults to "child"
				require.Equal(t, "child", payload.Arguments["placement"])
				require.True(t, strings.HasPrefix(payload.Arguments["title"].(string), "Review Fix "))

				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": `{"conversation_id": "conv-test-99", "agent_id": "ag-1", "title": "Review Fix Auth", "status": "running"}`},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-token")

		r := NewRegistry()
		r.SetOmnigentConfig(OmnigentLaunchConfig{
			PreferredHarnesses: []string{"antigravity"},
		})
		registerOmnigentTools(r)

		toolsMap := make(map[string]llm.ToolDef)
		for _, tool := range r.ToTools() {
			toolsMap[tool.Name] = tool
		}

		require.Contains(t, toolsMap, "omnigent_agent_capabilities")
		require.Contains(t, toolsMap, "omnigent_session_launch")

		// Call omnigent_agent_capabilities
		capCall := llm.ToolCall{
			ID:        "c1",
			Name:      "omnigent_agent_capabilities",
			Arguments: "{}",
		}
		capOut, err := r.HandleCall(context.Background(), capCall)
		require.NoError(t, err)
		require.NotContains(t, capOut, "cassandra")

		// Call omnigent_session_launch
		launchArgs, _ := json.Marshal(OmnigentSessionLaunchArgs{
			Title:   "Fix Auth Validation",
			Message: "Please fix token expiration logic",
		})
		launchCall := llm.ToolCall{
			ID:        "c2",
			Name:      "omnigent_session_launch",
			Arguments: string(launchArgs),
		}
		launchOut, err := r.HandleCall(context.Background(), launchCall)
		require.NoError(t, err)
		require.Contains(t, launchOut, "omnigent-session://conv-test-99")
		require.Contains(t, launchOut, "Review Fix Auth")
	})

	t.Run("placement override is respected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)

			if payload.Name == "sys_agent_list" {
				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": sampleOmnigentAgents()},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			} else if payload.Name == "sys_session_create" {
				require.Equal(t, "same_host", payload.Arguments["placement"])
				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": `{"conversation_id": "conv-top-1", "agent_id": "ag-1", "title": "Review Fix Bug", "status": "running"}`},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-token")

		r := NewRegistry()
		registerOmnigentTools(r)

		launchArgs, _ := json.Marshal(OmnigentSessionLaunchArgs{
			Title:     "Fix Bug",
			Message:   "Fix issue",
			Placement: "same_host",
		})
		launchCall := llm.ToolCall{
			ID:        "c3",
			Name:      "omnigent_session_launch",
			Arguments: string(launchArgs),
		}
		launchOut, err := r.HandleCall(context.Background(), launchCall)
		require.NoError(t, err)
		require.Contains(t, launchOut, "conv-top-1")
	})
}

func TestSessionTitlePrefix(t *testing.T) {
	t.Run("outside Omnigent session is no-op", func(t *testing.T) {
		t.Setenv("OMNIGENT", "")
		t.Setenv("OMNIGENT_RUNNER_WORKSPACE", "")
		t.Setenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID", "")
		t.Setenv("OMNIGENT_RELAY_URL", "")
		t.Setenv("OMNIGENT_RELAY_TOKEN", "")
		t.Setenv("OMNIGENT_BRIDGE_DIR", "")
		t.Setenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR", "")

		err := UpdateSessionTitlePrefix(context.Background(), ReviewStateApproved)
		require.NoError(t, err)
	})

	t.Run("inside Omnigent session updates title prefix", func(t *testing.T) {
		renamedTitle := ""
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)

			if payload.Name == "sys_session_get_info" {
				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": `{"title": "⏳ Review Add feature"}`},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			} else if payload.Name == "sys_session_rename" {
				renamedTitle = payload.Arguments["title"].(string)
				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": `{"status": "ok"}`},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-token")

		err := UpdateSessionTitlePrefix(context.Background(), ReviewStateApproved)
		require.NoError(t, err)
		require.Equal(t, "✅ Review Add feature", renamedTitle)
	})

	t.Run("idempotent when prefix is already set", func(t *testing.T) {
		renameCalled := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)

			if payload.Name == "sys_session_get_info" {
				resp := map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": `{"title": "✅ Review Add feature"}`},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			} else if payload.Name == "sys_session_rename" {
				renameCalled = true
			}
		}))
		defer server.Close()

		t.Setenv("OMNIGENT", "1")
		t.Setenv("OMNIGENT_RELAY_URL", server.URL)
		t.Setenv("OMNIGENT_RELAY_TOKEN", "test-token")

		err := UpdateSessionTitlePrefix(context.Background(), ReviewStateApproved)
		require.NoError(t, err)
		require.False(t, renameCalled)
	})
}

func TestFindActiveToolRelay(t *testing.T) {
	t.Run("matches target session ID among multiple sessions", func(t *testing.T) {
		root := t.TempDir()

		dir1 := filepath.Join(root, "host1", "session1")
		require.NoError(t, os.MkdirAll(dir1, 0o755))
		cfg1 := map[string]string{"url": "http://127.0.0.1:1001", "token": "tok1", "session_id": "sess-1"}
		raw1, _ := json.Marshal(cfg1)
		require.NoError(t, os.WriteFile(filepath.Join(dir1, "tool_relay.json"), raw1, 0o600))

		dir2 := filepath.Join(root, "host1", "session2")
		require.NoError(t, os.MkdirAll(dir2, 0o755))
		cfg2 := map[string]string{"url": "http://127.0.0.1:1002", "token": "tok2", "session_id": "sess-2"}
		raw2, _ := json.Marshal(cfg2)
		require.NoError(t, os.WriteFile(filepath.Join(dir2, "tool_relay.json"), raw2, 0o600))

		// Even if dir1 was written earlier, requesting sess-2 must find sess-2
		res := findActiveToolRelay(root, "sess-2")
		require.NotNil(t, res)
		require.Equal(t, "sess-2", res.SessionID)
		require.Equal(t, "http://127.0.0.1:1002", res.URL)
	})

	t.Run("falls back to untagged candidate if target session ID not found", func(t *testing.T) {
		root := t.TempDir()

		dirTagged := filepath.Join(root, "host1", "sess_tagged")
		require.NoError(t, os.MkdirAll(dirTagged, 0o755))
		cfgTagged := map[string]string{"url": "http://127.0.0.1:2001", "token": "tok-t", "session_id": "other-sess"}
		rawTagged, _ := json.Marshal(cfgTagged)
		require.NoError(t, os.WriteFile(filepath.Join(dirTagged, "tool_relay.json"), rawTagged, 0o600))

		dirUntagged := filepath.Join(root, "host1", "sess_untagged")
		require.NoError(t, os.MkdirAll(dirUntagged, 0o755))
		cfgUntagged := map[string]string{"url": "http://127.0.0.1:2002", "token": "tok-u"}
		rawUntagged, _ := json.Marshal(cfgUntagged)
		require.NoError(t, os.WriteFile(filepath.Join(dirUntagged, "tool_relay.json"), rawUntagged, 0o600))

		// Requesting non-existent target should NOT return other-sess, but instead return untagged
		res := findActiveToolRelay(root, "target-not-found")
		require.NotNil(t, res)
		require.Equal(t, "http://127.0.0.1:2002", res.URL)
		require.Empty(t, res.SessionID)
	})

	t.Run("returns nil if target session ID not found and no untagged candidate exists", func(t *testing.T) {
		root := t.TempDir()

		dirTagged := filepath.Join(root, "host1", "sess_tagged")
		require.NoError(t, os.MkdirAll(dirTagged, 0o755))
		cfgTagged := map[string]string{"url": "http://127.0.0.1:2001", "token": "tok-t", "session_id": "only-sess"}
		rawTagged, _ := json.Marshal(cfgTagged)
		require.NoError(t, os.WriteFile(filepath.Join(dirTagged, "tool_relay.json"), rawTagged, 0o600))

		res := findActiveToolRelay(root, "unknown-sess")
		require.Nil(t, res)
	})

	t.Run("falls back to newest tool_relay.env when no json config exists", func(t *testing.T) {
		root := t.TempDir()

		dirOld := filepath.Join(root, "host1", "old_sess")
		require.NoError(t, os.MkdirAll(dirOld, 0o755))
		envOld := "OMNIGENT_RELAY_URL=http://127.0.0.1:3001\nOMNIGENT_RELAY_TOKEN=old-tok\n"
		pathOld := filepath.Join(dirOld, "tool_relay.env")
		require.NoError(t, os.WriteFile(pathOld, []byte(envOld), 0o600))

		dirNew := filepath.Join(root, "host1", "new_sess")
		require.NoError(t, os.MkdirAll(dirNew, 0o755))
		envNew := "OMNIGENT_RELAY_URL=http://127.0.0.1:3002\nOMNIGENT_RELAY_TOKEN=new-tok\n"
		pathNew := filepath.Join(dirNew, "tool_relay.env")
		require.NoError(t, os.WriteFile(pathNew, []byte(envNew), 0o600))

		pastTime := time.Now().Add(-10 * time.Minute)
		require.NoError(t, os.Chtimes(pathOld, pastTime, pastTime))

		res := findActiveToolRelay(root, "")
		require.NotNil(t, res)
		require.Equal(t, "http://127.0.0.1:3002", res.URL)
		require.Equal(t, "new-tok", res.Token)
	})

	t.Run("bounded read does not panic on oversized file", func(t *testing.T) {
		root := t.TempDir()
		oversized := filepath.Join(root, "huge.json")
		data := make([]byte, 128*1024)
		for i := range data {
			data[i] = ' '
		}
		require.NoError(t, os.WriteFile(oversized, data, 0o600))

		cfg, err := readRelayConfigFile(oversized)
		require.Error(t, err)
		require.Nil(t, cfg)
	})
}

func TestMultiSourceOmnigentAgents(t *testing.T) {
	bTrue := true
	resp := OmnigentAgentListResponse{
		Builtins: []OmnigentAgentEntry{
			{AgentID: "ag-builtin-1", Name: "antigravity-native-ui", Harness: "antigravity-native", AvailableOnHost: &bTrue},
			{AgentID: "ag-cassandra-1", Name: "cassandra", Harness: "acp:cassandra", AvailableOnHost: &bTrue},
		},
		SessionAgents: []OmnigentAgentEntry{
			{AgentID: "ag-session-1", Name: "custom-session-agent", Harness: "custom-session", AvailableOnHost: &bTrue},
			{AgentID: "ag-cassandra-2", Name: "cassandra-session", Harness: "cassandra", AvailableOnHost: &bTrue},
		},
		LocalConfigs: []OmnigentAgentEntry{
			{AgentID: "ag-local-1", Name: "override-agent", Harness: "local-override", AvailableOnHost: &bTrue},
			{AgentID: "ag-builtin-1", Name: "antigravity-override", Harness: "local-antigravity", AvailableOnHost: &bTrue},
			{AgentID: "ag-cassandra-3", Name: "cassandra-local", Harness: "acp:cassandra-native", AvailableOnHost: &bTrue},
		},
	}
	raw, err := json.Marshal(resp)
	require.NoError(t, err)

	t.Run("MaskCassandraInOmnigentAgents masks Cassandra across all sources", func(t *testing.T) {
		masked := MaskCassandraInOmnigentAgents(string(raw))
		var unmarshaled OmnigentAgentListResponse
		require.NoError(t, json.Unmarshal([]byte(masked), &unmarshaled))

		for _, a := range unmarshaled.Builtins {
			require.False(t, strings.Contains(strings.ToLower(a.Name), "cassandra"))
		}
		for _, a := range unmarshaled.SessionAgents {
			require.False(t, strings.Contains(strings.ToLower(a.Name), "cassandra"))
		}
		for _, a := range unmarshaled.LocalConfigs {
			require.False(t, strings.Contains(strings.ToLower(a.Name), "cassandra"))
		}
	})

	t.Run("ResolveOmnigentLaunchAgent discovers and prioritizes local and session agents", func(t *testing.T) {
		cfg := OmnigentLaunchConfig{
			PreferredHarnesses: []string{"custom-session"},
		}
		resolved, err := ResolveOmnigentLaunchAgent(string(raw), cfg, "")
		require.NoError(t, err)
		require.Equal(t, "ag-session-1", resolved.AgentID)
		require.Equal(t, "custom-session-agent", resolved.Name)
	})

	t.Run("LocalConfigs take precedence over Builtins on duplicate agent ID", func(t *testing.T) {
		cfg := OmnigentLaunchConfig{
			PreferredHarnesses: []string{"antigravity"},
		}
		resolved, err := ResolveOmnigentLaunchAgent(string(raw), cfg, "")
		require.NoError(t, err)
		require.Equal(t, "ag-builtin-1", resolved.AgentID)
		require.Equal(t, "antigravity-override", resolved.Name)
		require.Equal(t, "local-antigravity", resolved.Harness)
	})
}

func TestOmnigentSessionLaunch_ExactOptionMatching(t *testing.T) {
	bTrue := true
	resp := OmnigentAgentListResponse{
		Builtins: []OmnigentAgentEntry{
			{AgentID: "claude-cli", Name: "claude", Harness: "claude-cli-harness", AvailableOnHost: &bTrue},
			{AgentID: "claude-native", Name: "claude-native-ui", Harness: "claude-native-harness", AvailableOnHost: &bTrue},
		},
	}
	rawAgents, err := json.Marshal(resp)
	require.NoError(t, err)

	var launchedAgentID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Name == "sys_agent_list" {
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": string(rawAgents)},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		} else if payload.Name == "sys_session_create" {
			launchedAgentID = payload.Arguments["agent_id"].(string)
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `{"conversation_id": "conv-exact-1"}`},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	t.Setenv("OMNIGENT", "1")
	t.Setenv("OMNIGENT_RELAY_URL", server.URL)
	t.Setenv("OMNIGENT_RELAY_TOKEN", "test-token")

	r := NewRegistry()
	r.SetOmnigentConfig(OmnigentLaunchConfig{
		PromptForHarness: true,
	})
	prompter := &testPrompter{
		response: AskDeveloperResponse{
			Status:   "answered",
			Selected: []string{"claude-native-ui (claude-native-harness)"},
		},
	}
	r.SetDeveloperPrompter(prompter)
	registerOmnigentTools(r)

	launchCall := llm.ToolCall{
		ID:        "c_exact",
		Name:      "omnigent_session_launch",
		Arguments: `{"title": "Fix Prefix", "message": "fix it"}`,
	}
	out, err := r.HandleCall(context.Background(), launchCall)
	require.NoError(t, err)
	require.Contains(t, out, "conv-exact-1")
	require.True(t, prompter.called)
	// Must have chosen claude-native, NOT claude
	require.Equal(t, "claude-native", launchedAgentID)
}
