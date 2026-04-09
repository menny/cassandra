package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindRepoFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create root AGENTS.md
	rootAgents := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(rootAgents, []byte("root rules"), 0o644))

	// Create nested REVIEWERS.md
	nestedDir := filepath.Join(tmpDir, "pkg", "core")
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	nestedReviewers := filepath.Join(nestedDir, "REVIEWERS.md")
	require.NoError(t, os.WriteFile(nestedReviewers, []byte("nested reviewers"), 0o644))

	t.Run("finds root agent and nested reviewer", func(t *testing.T) {
		changedFiles := []string{
			"main.go",
			"pkg/core/logic.go",
		}
		agents := findRepoFiles(tmpDir, changedFiles, "AGENTS.md")
		reviewers := findRepoFiles(tmpDir, changedFiles, "REVIEWERS.md")

		require.Len(t, agents, 1)
		require.Contains(t, agents, "/")
		require.Equal(t, "root rules", agents["/"])

		require.Len(t, reviewers, 1)
		require.Contains(t, reviewers, filepath.Join("pkg", "core"))
		require.Equal(t, "nested reviewers", reviewers[filepath.Join("pkg", "core")])
	})
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Inject a REVIEWERS.md to verify it's placed inside code_review_guidelines
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "REVIEWERS.md"), []byte("SOME REVIEWERS"), 0o644))
	// Inject an AGENTS.md too
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("SOME AGENTS"), 0o644))

	changedFiles := []string{"foo.go"}

	prompt, err := BuildSystemPrompt(tmpDir, changedFiles, "Is this code maintainable, easy to work with, and safe?")
	require.NoError(t, err)

	require.True(t, strings.Contains(prompt, "You are a code review bot - named Cassandra - for the provided codebase."))
	require.True(t, strings.Contains(prompt, "<code_review_guidelines>"))
	require.True(t, strings.Contains(prompt, "Is this code maintainable, easy to work with, and safe?"))
	require.True(t, strings.Contains(prompt, "Skepticism of Internal Knowledge"))

	// Check that reviewers is inside code_review_guidelines:
	guidelinesIndex := strings.Index(prompt, "<code_review_guidelines>")
	endGuidelinesIndex := strings.Index(prompt, "</code_review_guidelines>")
	reviewersIndex := strings.Index(prompt, "SOME REVIEWERS")
	require.True(t, reviewersIndex > guidelinesIndex && reviewersIndex < endGuidelinesIndex, "REVIEWERS.md content should be inside code_review_guidelines")

	// Check AGENTS.md
	require.True(t, strings.Contains(prompt, "SOME AGENTS"))
	require.True(t, strings.Contains(prompt, "<agents_guidelines>"))

	// Check folder paths print:
	require.True(t, strings.Contains(prompt, "Directory: /"))
}

func TestBuildSystemPrompt_Override(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	prompt, err := BuildSystemPrompt(tmpDir, nil, "CUSTOM GUIDELINES HERE")
	require.NoError(t, err)

	require.True(t, strings.Contains(prompt, "You are a code review bot - named Cassandra - for the provided codebase."))
	require.True(t, strings.Contains(prompt, "<code_review_guidelines>"))
	require.True(t, strings.Contains(prompt, "CUSTOM GUIDELINES HERE"))
}
