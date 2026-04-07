package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMainGuidelinesContent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a local file
	localFile := filepath.Join(tmpDir, "my_rules.md")
	localContent := "local rules content"
	require.NoError(t, os.WriteFile(localFile, []byte(localContent), 0o644))

	t.Run("resolves local file path", func(t *testing.T) {
		content, err := resolveMainGuidelinesContent(localFile)
		require.NoError(t, err)
		require.Equal(t, localContent, content)
	})

	t.Run("resolves named prompt from embedded library", func(t *testing.T) {
		content, err := resolveMainGuidelinesContent("google")
		require.NoError(t, err)
		require.Contains(t, content, "Google Engineering Practices")
	})

	t.Run("fails on non-existent path and name", func(t *testing.T) {
		_, err := resolveMainGuidelinesContent("non-existent-at-all")
		require.Error(t, err)
		require.Contains(t, err.Error(), "prompt \"non-existent-at-all\" not found in library")
	})
}
