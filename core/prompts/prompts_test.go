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

	stable, dynamic, _, err := BuildSystemPrompt(tmpDir, changedFiles, "Is this code maintainable, easy to work with, and safe?", "")
	require.NoError(t, err)

	// Combine for checks that don't care about the split point.
	prompt := stable + dynamic

	require.True(t, strings.Contains(prompt, "You are a code review bot - named Cassandra - for the provided codebase."))
	require.True(t, strings.Contains(prompt, "<code_review_guidelines>"))
	require.True(t, strings.Contains(prompt, "Is this code maintainable, easy to work with, and safe?"))
	require.True(t, strings.Contains(prompt, "Skepticism of Internal Knowledge"))
	require.True(t, strings.Contains(prompt, "<approval_evaluation_guidelines>"))
	require.True(t, strings.Contains(prompt, "Approve"))
	require.True(t, strings.Contains(prompt, "Reject"))
	require.True(t, strings.Contains(prompt, "Comment"))

	// REVIEWERS.md content must appear in <reviewer_context>, which comes AFTER </code_review_guidelines>.
	endGuidelinesIndex := strings.Index(prompt, "</code_review_guidelines>")
	reviewersIndex := strings.Index(prompt, "SOME REVIEWERS")
	require.True(t, reviewersIndex > endGuidelinesIndex, "REVIEWERS.md content should be outside (after) code_review_guidelines")
	require.True(t, strings.Contains(prompt, "<reviewer_context>"))
	require.True(t, strings.Contains(prompt, "</reviewer_context>"))

	// Check AGENTS.md
	require.True(t, strings.Contains(prompt, "SOME AGENTS"))
	require.True(t, strings.Contains(prompt, "<agents_guidelines>"))

	// Check folder paths print:
	require.True(t, strings.Contains(prompt, "Directory: /"))

	// Dynamic content must be non-empty and stable must not contain Zone 3 sections.
	require.NotEmpty(t, dynamic, "dynamic should contain AGENTS.md and REVIEWERS.md sections")
	require.False(t, strings.Contains(stable, "SOME REVIEWERS"), "stable should not contain dynamic REVIEWERS.md content")
	require.False(t, strings.Contains(stable, "SOME AGENTS"), "stable should not contain dynamic AGENTS.md content")
}

// TestBuildSystemPrompt_DeterministicZone3Ordering verifies that when multiple
// AGENTS.md or REVIEWERS.md files are discovered the dynamic suffix always lists
// them in sorted (ascending) path order, regardless of Go's randomized map
// iteration order.
func TestBuildSystemPrompt_DeterministicZone3Ordering(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Create three sub-directories each containing an AGENTS.md and a REVIEWERS.md.
	// Use names that would easily surface ordering bugs: gamma < beta < alpha is
	// reverse-alphabetical, so any test relying on insertion order would catch it.
	dirs := []string{"alpha", "beta", "gamma"}
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, d), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(tmpDir, d, "AGENTS.md"),
			[]byte(d+" agents content"),
			0o644,
		))
		require.NoError(t, os.WriteFile(
			filepath.Join(tmpDir, d, "REVIEWERS.md"),
			[]byte(d+" reviewers content"),
			0o644,
		))
	}

	changedFiles := []string{"alpha/a.go", "beta/b.go", "gamma/c.go"}

	// Run BuildSystemPrompt multiple times to catch non-determinism that only
	// appears probabilistically due to random map iteration.
	const runs = 20
	var firstDynamic string
	for i := range runs {
		_, dynamic, _, err := BuildSystemPrompt(tmpDir, changedFiles, "guidelines", "")
		require.NoError(t, err)
		if i == 0 {
			firstDynamic = dynamic
		} else {
			require.Equal(t, firstDynamic, dynamic,
				"dynamic Zone 3 output differs between run 0 and run %d — non-deterministic ordering detected", i)
		}
	}

	// Also assert the concrete sorted order: alpha → beta → gamma.
	alphaAgentsIdx := strings.Index(firstDynamic, "alpha agents content")
	betaAgentsIdx := strings.Index(firstDynamic, "beta agents content")
	gammaAgentsIdx := strings.Index(firstDynamic, "gamma agents content")
	require.True(t, alphaAgentsIdx < betaAgentsIdx,
		"alpha/AGENTS.md should appear before beta/AGENTS.md in dynamic output")
	require.True(t, betaAgentsIdx < gammaAgentsIdx,
		"beta/AGENTS.md should appear before gamma/AGENTS.md in dynamic output")

	alphaReviewersIdx := strings.Index(firstDynamic, "alpha reviewers content")
	betaReviewersIdx := strings.Index(firstDynamic, "beta reviewers content")
	gammaReviewersIdx := strings.Index(firstDynamic, "gamma reviewers content")
	require.True(t, alphaReviewersIdx < betaReviewersIdx,
		"alpha/REVIEWERS.md should appear before beta/REVIEWERS.md in dynamic output")
	require.True(t, betaReviewersIdx < gammaReviewersIdx,
		"beta/REVIEWERS.md should appear before gamma/REVIEWERS.md in dynamic output")
}

func TestBuildSystemPrompt_Override(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	stable, dynamic, _, err := BuildSystemPrompt(tmpDir, nil, "CUSTOM GUIDELINES HERE", "CUSTOM APPROVAL HERE")
	require.NoError(t, err)

	require.True(t, strings.Contains(stable, "You are a code review bot - named Cassandra - for the provided codebase."))
	require.True(t, strings.Contains(stable, "<code_review_guidelines>"))
	require.True(t, strings.Contains(stable, "CUSTOM GUIDELINES HERE"))
	require.True(t, strings.Contains(stable, "<approval_evaluation_guidelines>"))
	require.True(t, strings.Contains(stable, "CUSTOM APPROVAL HERE"))

	// No AGENTS.md or REVIEWERS.md → dynamic should be empty.
	require.Empty(t, dynamic, "dynamic should be empty when no Zone 3 files exist")
}
