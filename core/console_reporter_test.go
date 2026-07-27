package core

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestConsoleReporter_Raw(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	r := NewRawReporter(&stdout, &stderr)

	t.Run("NotifyUser is no-op", func(t *testing.T) {
		stderr.Reset()
		r.NotifyUser()
		require.Empty(t, stderr.String())
	})

	t.Run("ReportIteration prints plain text", func(t *testing.T) {
		stderr.Reset()
		r.ReportIteration(3)
		require.Equal(t, "🔍 [Iter 3] Reviewing...\n", stderr.String())
	})

	t.Run("ReportReview prints plain review", func(t *testing.T) {
		stdout.Reset()
		err := r.ReportReview("Hello World")
		require.NoError(t, err)
		require.Equal(t, "Hello World\n", stdout.String())
	})

	t.Run("ReportReviewHeader prints plain header", func(t *testing.T) {
		stderr.Reset()
		r.ReportReviewHeader(2, "general", "model-id")
		require.Contains(t, stderr.String(), "Review generated successfully")
		require.Contains(t, stderr.String(), "Review for 2 files")
	})

	t.Run("ReportConfig prints standard config", func(t *testing.T) {
		stderr.Reset()
		cfg := config.NewDefaultConfig()
		cfg.Provider = "google"
		cfg.Model = "gemini-1.5-flash"
		r.ReportConfig(cfg, "/workspace")
		require.Contains(t, stderr.String(), "google")
		require.Contains(t, stderr.String(), "gemini-1.5-flash")
	})

	t.Run("ReportToolCalls standard", func(t *testing.T) {
		stderr.Reset()
		r.ReportToolCalls([]llm.ToolCall{
			{Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
		})
		require.Contains(t, stderr.String(), "* 🛠️  [Tool] read_file({\"file_path\":\"foo.go\"})")
	})

	t.Run("ReportToolCalls reviewer state", func(t *testing.T) {
		stderr.Reset()
		args, err := json.Marshal(map[string]string{
			"message":    "Analyzing files",
			"focus_area": "security",
		})
		require.NoError(t, err)

		r.ReportToolCalls([]llm.ToolCall{
			{Name: "emit_reviewer_state", Arguments: string(args)},
		})
		require.Contains(t, stderr.String(), "[Reviewer state] focus area: security")
		require.Contains(t, stderr.String(), "Analyzing files")
	})

	t.Run("ReportStageIteration prints stage name", func(t *testing.T) {
		stderr.Reset()
		r.ReportStageIteration("Pre-Review Summary", 2)
		require.Equal(t, "🔍 [Pre-Review Summary - Iter 2] Reviewing...\n", stderr.String())
	})

	t.Run("ReportStageFinalReview prints stage name", func(t *testing.T) {
		stderr.Reset()
		r.ReportStageFinalReview("Pre-Review Summary")
		require.Equal(t, "📝 Formulating final pre-review summary...\n", stderr.String())
	})

	t.Run("ReportStageReview prints header and result", func(t *testing.T) {
		stderr.Reset()
		err := r.ReportStageReview("Pre-Review Summary", "Summary content")
		require.NoError(t, err)
		require.Contains(t, stderr.String(), "# 📋 Pre-Review Summary")
		require.Contains(t, stderr.String(), "Summary content")
	})

	t.Run("ReportPostReviewUserQuery prints user query", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewUserQuery("How does auth work?")
		require.Contains(t, stderr.String(), "💬 User:\nHow does auth work?")
	})

	t.Run("ReportPostReviewReply prints reply", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewReply("Auth uses JWT tokens.")
		require.Equal(t, "Auth uses JWT tokens.\n", stderr.String())
	})

	t.Run("ReportConfig prints pre-review prompt and model", func(t *testing.T) {
		stderr.Reset()
		cfg := config.NewDefaultConfig()
		cfg.PreReviewPromptFile = ".cassandra/pre-review-summary.md"
		cfg.PreReviewModel = "gemini-2.0-flash"
		r.ReportConfig(cfg, "/workspace")
		require.Contains(t, stderr.String(), "Pre-Review Prompt File")
		require.Contains(t, stderr.String(), ".cassandra/pre-review-summary.md")
		require.Contains(t, stderr.String(), "Pre-Review Model")
		require.Contains(t, stderr.String(), "gemini-2.0-flash")
	})
}

func TestConsoleReporter_Markdown(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	r := NewMarkdownReporter(&stdout, &stderr)

	t.Run("NotifyUser writes bell to stderr", func(t *testing.T) {
		stderr.Reset()
		r.NotifyUser()
		require.Equal(t, "\a", stderr.String())
	})

	t.Run("ReportIteration prints styled text", func(t *testing.T) {
		stderr.Reset()
		r.ReportIteration(3)
		require.Contains(t, stderr.String(), "[Iteration 3]")
	})

	t.Run("ReportReview prints formatted markdown review", func(t *testing.T) {
		stdout.Reset()
		err := r.ReportReview("# Header\n- list item")
		require.NoError(t, err)
		// glamour rendered output
		require.Contains(t, stdout.String(), "Header")
		require.Contains(t, stdout.String(), "list item")
	})

	t.Run("ReportReviewHeader prints styled header", func(t *testing.T) {
		stderr.Reset()
		r.ReportReviewHeader(2, "general", "model-id")
		require.Contains(t, stderr.String(), "Review for 2 files")
	})

	t.Run("ReportToolCalls standard", func(t *testing.T) {
		stderr.Reset()
		r.ReportToolCalls([]llm.ToolCall{
			{Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
		})
		require.Contains(t, stderr.String(), "read_file")
	})

	t.Run("ReportToolCalls reviewer state", func(t *testing.T) {
		stderr.Reset()
		args, err := json.Marshal(map[string]string{
			"message":    "Analyzing files",
			"focus_area": "security",
		})
		require.NoError(t, err)

		r.ReportToolCalls([]llm.ToolCall{
			{Name: "emit_reviewer_state", Arguments: string(args)},
		})
		require.Contains(t, stderr.String(), "Reviewer state")
		require.Contains(t, stderr.String(), "security")
	})

	t.Run("ReportStageIteration prints styled stage text", func(t *testing.T) {
		stderr.Reset()
		r.ReportStageIteration("Pre-Review Summary", 2)
		require.Contains(t, stderr.String(), "[Pre-Review Summary - Iteration 2]")
	})

	t.Run("ReportStageFinalReview prints styled stage final review text", func(t *testing.T) {
		stderr.Reset()
		r.ReportStageFinalReview("Pre-Review Summary")
		require.Contains(t, stderr.String(), "Formulating final pre-review summary...")
	})

	t.Run("ReportStageReview prints styled stage review markdown", func(t *testing.T) {
		stderr.Reset()
		err := r.ReportStageReview("Pre-Review Summary", "Summary content")
		require.NoError(t, err)
		require.Contains(t, stderr.String(), "Pre-Review Summary")
	})

	t.Run("ReportPostReviewUserQuery prints styled user query box", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewUserQuery("How does auth work?")
		require.Contains(t, stderr.String(), "User:")
		require.Contains(t, stderr.String(), "How does auth work?")
	})

	t.Run("ReportPostReviewReply prints styled reply markdown", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewReply("Auth uses JWT tokens.")
		require.Contains(t, stderr.String(), "Auth uses JWT tokens.")
	})
}
