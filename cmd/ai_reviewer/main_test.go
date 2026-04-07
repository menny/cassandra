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

	// Create a "library" prompt
	libDir := filepath.Join(tmpDir, "reviewer_prompts")
	require.NoError(t, os.MkdirAll(libDir, 0o755))
	libFile := filepath.Join(libDir, "google.md")
	libContent := "google rules content"
	require.NoError(t, os.WriteFile(libFile, []byte(libContent), 0o644))

	t.Run("resolves local file path", func(t *testing.T) {
		content, err := resolveMainGuidelinesContent(localFile, tmpDir)
		require.NoError(t, err)
		require.Equal(t, localContent, content)
	})

	t.Run("resolves named prompt from library", func(t *testing.T) {
		content, err := resolveMainGuidelinesContent("google", tmpDir)
		require.NoError(t, err)
		require.Equal(t, libContent, content)
	})

	t.Run("fails on non-existent path and name", func(t *testing.T) {
		_, err := resolveMainGuidelinesContent("non-existent", tmpDir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to resolve main guidelines")
	})
}
