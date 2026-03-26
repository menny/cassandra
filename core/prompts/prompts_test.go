package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindAgentsMDFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create root AGENTS.md
	rootAgents := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(rootAgents, []byte("root rules"), 0o644))

	// Create nested AGENTS.md
	nestedDir := filepath.Join(tmpDir, "pkg", "core")
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	nestedAgents := filepath.Join(nestedDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(nestedAgents, []byte("nested rules"), 0o644))

	t.Run("finds root agent and nested agent", func(t *testing.T) {
		changedFiles := []string{
			"main.go",
			"pkg/core/logic.go",
		}
		found := findAgentsMDFiles(tmpDir, changedFiles)

		require.Len(t, found, 2)
		require.Contains(t, found, "AGENTS.md")
		require.Contains(t, found, filepath.Join("pkg", "core", "AGENTS.md"))
		require.Equal(t, "root rules", found["AGENTS.md"])
		require.Equal(t, "nested rules", found[filepath.Join("pkg", "core", "AGENTS.md")])
	})
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	prompt, err := BuildSystemPrompt(tmpDir, nil)
	require.NoError(t, err)

	require.True(t, strings.Contains(prompt, "You are a code review bot for the provided codebase."))
	require.True(t, strings.Contains(prompt, "<code_review_guidelines>"))
	require.True(t, strings.Contains(prompt, "Is this code maintainable, easy to work with, and safe?"))
}
