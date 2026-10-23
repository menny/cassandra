package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestNewSessionStore(t *testing.T) {
	t.Run("empty config folder returns NoopSessionStore", func(t *testing.T) {
		store := NewSessionStore("")
		require.False(t, store.SessionExists("any"))
		_, err := store.LoadSession(context.Background(), "any")
		require.Error(t, err)
		require.Contains(t, err.Error(), "session persistence is disabled")
		require.NoError(t, store.AppendMessages(context.Background(), "any", llm.Message{}))
	})

	t.Run("whitespace config folder returns NoopSessionStore", func(t *testing.T) {
		store := NewSessionStore("   ")
		require.False(t, store.SessionExists("any"))
	})

	t.Run("non-empty config folder returns ConcreteSessionStore", func(t *testing.T) {
		tmpDir := t.TempDir()
		store := NewSessionStore(tmpDir)
		require.NotNil(t, store)
		require.False(t, store.SessionExists("nonexistent"))
	})
}

func TestConcreteSessionStore_AppendAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewConcreteSessionStore(tmpDir)
	ctx := context.Background()
	sid := "sess_test_1"

	require.False(t, store.SessionExists(sid))

	msg1 := llm.Message{
		Role: llm.RoleSystem,
		Text: "You are Cassandra code reviewer.",
	}
	msg2 := llm.Message{
		Role: llm.RoleUser,
		Text: "Please review this diff.",
	}
	msg3 := llm.Message{
		Role:      llm.RoleAssistant,
		Text:      "I will check the files.",
		Reasoning: "Examining changed lines",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
		},
		ProviderMetadata: map[string]any{"thought_signature": "sig123"},
	}
	msg4 := llm.Message{
		Role: llm.RoleTool,
		ToolResults: []llm.ToolResult{
			{ToolCallID: "call_1", Name: "read_file", Content: "package main\nfunc main() {}"},
		},
	}
	msg5 := llm.Message{
		Role: llm.RoleAssistant,
		Text: "LGTM!",
	}

	// Append individual message
	require.NoError(t, store.AppendMessages(ctx, sid, msg1))
	require.True(t, store.SessionExists(sid))

	// Verify permissions
	sessionDir := filepath.Join(tmpDir, "sessions", sid)
	messagesFile := filepath.Join(sessionDir, "messages.jsonl")

	dirInfo, err := os.Stat(sessionDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	fileInfo, err := os.Stat(messagesFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())

	// Append batch of remaining messages
	require.NoError(t, store.AppendMessages(ctx, sid, msg2, msg3, msg4, msg5))

	// Load session and verify all messages match
	loaded, err := store.LoadSession(ctx, sid)
	require.NoError(t, err)
	require.Len(t, loaded, 5)

	require.Equal(t, msg1.Role, loaded[0].Role)
	require.Equal(t, msg1.Text, loaded[0].Text)

	require.Equal(t, msg2.Role, loaded[1].Role)
	require.Equal(t, msg2.Text, loaded[1].Text)

	require.Equal(t, msg3.Role, loaded[2].Role)
	require.Equal(t, msg3.Text, loaded[2].Text)
	require.Equal(t, msg3.Reasoning, loaded[2].Reasoning)
	require.Len(t, loaded[2].ToolCalls, 1)
	require.Equal(t, "call_1", loaded[2].ToolCalls[0].ID)
	require.Equal(t, "read_file", loaded[2].ToolCalls[0].Name)
	require.Equal(t, `{"path":"main.go"}`, loaded[2].ToolCalls[0].Arguments)

	require.Equal(t, msg4.Role, loaded[3].Role)
	require.Len(t, loaded[3].ToolResults, 1)
	require.Equal(t, "call_1", loaded[3].ToolResults[0].ToolCallID)
	require.Equal(t, "read_file", loaded[3].ToolResults[0].Name)
	require.Equal(t, "package main\nfunc main() {}", loaded[3].ToolResults[0].Content)

	require.Equal(t, msg5.Role, loaded[4].Role)
	require.Equal(t, "LGTM!", loaded[4].Text)

	// Verify that whitespace-padded session ID resolves to the exact same session
	require.True(t, store.SessionExists("  "+sid+"  "))
	loadedPadded, err := store.LoadSession(ctx, "  "+sid+"  ")
	require.NoError(t, err)
	require.Len(t, loadedPadded, 5)
}

func TestConcreteSessionStore_InvalidSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewConcreteSessionStore(tmpDir)
	ctx := context.Background()

	invalidIDs := []string{
		"",
		"   ",
		"../escape",
		"nested/dir",
		`win\path`,
		".",
		"..",
	}

	for _, id := range invalidIDs {
		t.Run("id_"+id, func(t *testing.T) {
			require.Error(t, store.AppendMessages(ctx, id, llm.Message{Text: "hi"}))
			_, err := store.LoadSession(ctx, id)
			require.Error(t, err)
			require.False(t, store.SessionExists(id))
		})
	}
}

func TestConcreteSessionStore_LoadErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewConcreteSessionStore(tmpDir)
	ctx := context.Background()

	t.Run("session not found", func(t *testing.T) {
		_, err := store.LoadSession(ctx, "nonexistent")
		require.Error(t, err)
		require.ErrorIs(t, err, ErrSessionNotFound)
		require.Contains(t, err.Error(), "not found")
	})

	t.Run("empty session file", func(t *testing.T) {
		sessDir := filepath.Join(tmpDir, "sessions", "empty_sess")
		require.NoError(t, os.MkdirAll(sessDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(sessDir, "messages.jsonl"), []byte("   \n\n"), 0o600))

		_, err := store.LoadSession(ctx, "empty_sess")
		require.Error(t, err)
		require.ErrorIs(t, err, ErrSessionEmpty)
		require.Contains(t, err.Error(), "is empty")
	})

	t.Run("corrupt json line", func(t *testing.T) {
		sessDir := filepath.Join(tmpDir, "sessions", "corrupt_sess")
		require.NoError(t, os.MkdirAll(sessDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(sessDir, "messages.jsonl"), []byte("{\"role\":\"user\"}\n{bad json}\n"), 0o600))

		_, err := store.LoadSession(ctx, "corrupt_sess")
		require.Error(t, err)
		require.Contains(t, err.Error(), "corrupt message on line 2")
	})
}

func TestConcreteSessionStore_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewConcreteSessionStore(tmpDir)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.AppendMessages(canceledCtx, "sess_1", llm.Message{Text: "test"})
	require.ErrorIs(t, err, context.Canceled)

	_, err = store.LoadSession(canceledCtx, "sess_1")
	require.ErrorIs(t, err, context.Canceled)
}
