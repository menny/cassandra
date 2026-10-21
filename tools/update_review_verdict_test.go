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

func TestUpdateReviewVerdict_ValidTransitions(t *testing.T) {
	tests := []struct {
		name            string
		verdictInput    string
		rationaleInput  string
		expectedVerdict string
		expectedEmoji   string
		expectListenerV string
		expectListenerR string
	}{
		{
			name:            "approved lowercase",
			verdictInput:    "approved",
			rationaleInput:  "Author added missing bounds checks.",
			expectedVerdict: "approved",
			expectedEmoji:   ReviewStateApproved,
			expectListenerV: "approved",
			expectListenerR: "Author added missing bounds checks.",
		},
		{
			name:            "approve prefix",
			verdictInput:    "approve",
			rationaleInput:  "Thread safety is assured by external mutex.",
			expectedVerdict: "approved",
			expectedEmoji:   ReviewStateApproved,
			expectListenerV: "approved",
			expectListenerR: "Thread safety is assured by external mutex.",
		},
		{
			name:            "approved emoji format",
			verdictInput:    "✅ Approved",
			rationaleInput:  "Everything looks good now.",
			expectedVerdict: "approved",
			expectedEmoji:   ReviewStateApproved,
			expectListenerV: "approved",
			expectListenerR: "Everything looks good now.",
		},
		{
			name:            "rejected lowercase",
			verdictInput:    "rejected",
			rationaleInput:  "Race condition still present in worker pool.",
			expectedVerdict: "rejected",
			expectedEmoji:   ReviewStateRejected,
			expectListenerV: "rejected",
			expectListenerR: "Race condition still present in worker pool.",
		},
		{
			name:            "reject emoji format",
			verdictInput:    "❌ Rejected",
			rationaleInput:  "Critical security leak unpatched.",
			expectedVerdict: "rejected",
			expectedEmoji:   ReviewStateRejected,
			expectListenerV: "rejected",
			expectListenerR: "Critical security leak unpatched.",
		},
		{
			name:            "comment lowercase",
			verdictInput:    "comment",
			rationaleInput:  "Waiting for architectural clarification on caching.",
			expectedVerdict: "comment",
			expectedEmoji:   ReviewStateElse,
			expectListenerV: "comment",
			expectListenerR: "Waiting for architectural clarification on caching.",
		},
		{
			name:            "comment emoji format",
			verdictInput:    "💬 Comment",
			rationaleInput:  "Only stylistic suggestions remain.",
			expectedVerdict: "comment",
			expectedEmoji:   ReviewStateElse,
			expectListenerV: "comment",
			expectListenerR: "Only stylistic suggestions remain.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			registerUpdateReviewVerdict(r)

			var capturedVerdict, capturedRationale string
			r.SetVerdictChangeListener(func(ctx context.Context, verdict string, rationale string) error {
				capturedVerdict = verdict
				capturedRationale = rationale
				return nil
			})

			argsBytes, err := json.Marshal(map[string]any{
				"verdict":   tt.verdictInput,
				"rationale": tt.rationaleInput,
			})
			require.NoError(t, err)

			tc := llm.ToolCall{
				ID:        "call_1",
				Name:      "update_review_verdict",
				Arguments: string(argsBytes),
			}

			res, err := r.HandleCall(context.Background(), tc)
			require.NoError(t, err)
			require.Contains(t, res, tt.expectedVerdict)
			require.Contains(t, res, tt.rationaleInput)
			require.Equal(t, tt.expectListenerV, capturedVerdict)
			require.Equal(t, tt.expectListenerR, capturedRationale)
		})
	}
}

func TestUpdateReviewVerdict_ValidationErrors(t *testing.T) {
	t.Run("empty rationale", func(t *testing.T) {
		r := NewRegistry()
		registerUpdateReviewVerdict(r)

		argsBytes, err := json.Marshal(map[string]any{
			"verdict":   "approved",
			"rationale": "   ",
		})
		require.NoError(t, err)

		tc := llm.ToolCall{
			ID:        "call_err1",
			Name:      "update_review_verdict",
			Arguments: string(argsBytes),
		}

		_, err = r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "rationale is required")
	})

	t.Run("invalid verdict", func(t *testing.T) {
		r := NewRegistry()
		registerUpdateReviewVerdict(r)

		argsBytes, err := json.Marshal(map[string]any{
			"verdict":   "maybe_later",
			"rationale": "not sure",
		})
		require.NoError(t, err)

		tc := llm.ToolCall{
			ID:        "call_err2",
			Name:      "update_review_verdict",
			Arguments: string(argsBytes),
		}

		_, err = r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid verdict")
		require.Contains(t, err.Error(), "must be one of 'approved', 'rejected', or 'comment'")
	})
}

func TestUpdateReviewVerdict_T3Session(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "calls.log")

	script := `#!/bin/sh
cmd="$1"
shift
echo "$cmd $@" >> "` + logFile + `"
case "$cmd" in
  t3_thread_list)
    cat <<'EOF'
{
  "content": [{"type": "text", "text": "{\"threads\": [{\"id\": \"thread-123\", \"title\": \"Review my PR\"}], \"currentThreadId\": \"thread-123\"}"}],
  "isError": false
}
EOF
    ;;
  t3_thread_update)
    cat <<'EOF'
{
  "content": [{"type": "text", "text": "{\"id\": \"thread-123\", \"title\": \"updated\"}"}],
  "isError": false
}
EOF
    ;;
esac
`
	mcpScript := filepath.Join(tmpDir, "acp-mcp-call")
	require.NoError(t, os.WriteFile(mcpScript, []byte(script), 0o755))

	origNode := os.Getenv("T3_ACP_MCP_NODE")
	t.Setenv("T3_ACP_MCP_NODE", mcpScript)
	defer func() {
		if origNode == "" {
			_ = os.Unsetenv("T3_ACP_MCP_NODE")
		} else {
			_ = os.Setenv("T3_ACP_MCP_NODE", origNode)
		}
	}()

	r := NewRegistry()
	registerUpdateReviewVerdict(r)

	argsBytes, err := json.Marshal(map[string]any{
		"verdict":   "approved",
		"rationale": "Author resolved all blockers.",
	})
	require.NoError(t, err)

	tc := llm.ToolCall{
		ID:        "call_t3",
		Name:      "update_review_verdict",
		Arguments: string(argsBytes),
	}

	res, err := r.HandleCall(context.Background(), tc)
	require.NoError(t, err)
	require.Contains(t, res, "approved")
	require.Contains(t, res, ReviewStateApproved)

	logBytes, err := os.ReadFile(logFile)
	require.NoError(t, err)
	require.Contains(t, string(logBytes), "t3_thread_update")
	require.Contains(t, string(logBytes), ReviewStateApproved)
}
