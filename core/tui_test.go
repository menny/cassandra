package core

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTUIReporter_PostReviewMethods(t *testing.T) {
	var stderr bytes.Buffer
	r := NewTuiReporter(&stderr, &stderr, nil)

	t.Run("ReportPostReviewUserQuery formats user query box", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewUserQuery("How does auth work?")
		require.Contains(t, stderr.String(), "User:")
		require.Contains(t, stderr.String(), "How does auth work?")
	})

	t.Run("ReportPostReviewReply renders reply markdown", func(t *testing.T) {
		stderr.Reset()
		r.ReportPostReviewReply("Auth uses JWT.")
		require.Contains(t, stderr.String(), "Auth uses JWT.")
	})
}

func TestTUIModel_Update_StageMessages(t *testing.T) {
	t.Run("iterationMsg with stage updates model iterations", func(t *testing.T) {
		m := &tuiModel{
			mcpServers: make(map[string]*mcpServerState),
		}

		updatedModel, _ := m.Update(iterationMsg{iter: 1, stage: "Pre-Review Summary"})
		tm := updatedModel.(*tuiModel)
		require.Equal(t, 1, len(tm.iterations))
		require.Equal(t, 1, tm.iterations[0].iter)
		require.Equal(t, "Pre-Review Summary", tm.iterations[0].stage)
	})

	t.Run("preReviewSummaryMsg updates preReviewSummary state", func(t *testing.T) {
		m := &tuiModel{
			mcpServers: make(map[string]*mcpServerState),
		}

		updatedModel, _ := m.Update(preReviewSummaryMsg{summary: "Pre-review findings"})
		tm := updatedModel.(*tuiModel)
		require.Equal(t, "Pre-review findings", tm.preReviewSummary)
	})
}

func TestTUIModel_RenderContent_PreReviewSummary(t *testing.T) {
	t.Run("renders summary before AI Code Review stage iteration", func(t *testing.T) {
		m := &tuiModel{
			preReviewSummary: "This PR adds JWT auth.",
			iterations: []*iterationState{
				{iter: 1, stage: "Pre-Review Summary", llmStatus: "Done"},
				{iter: 1, stage: "AI Code Review", llmStatus: "Waiting for LLM reply..."},
			},
		}

		content := m.renderContent()
		require.Contains(t, content, "📋 Pre-Review Summary:")
		require.Contains(t, content, "This PR adds JWT auth.")
		require.Contains(t, content, "🔍 [Pre-Review Summary - Iteration 1]")
		require.Contains(t, content, "🔍 [AI Code Review - Iteration 1]")
	})

	t.Run("renders summary at end if AI Code Review stage iteration absent", func(t *testing.T) {
		m := &tuiModel{
			preReviewSummary: "Standalone pre-review summary text.",
			iterations: []*iterationState{
				{iter: 1, stage: "Pre-Review Summary", llmStatus: "Done"},
			},
		}

		content := m.renderContent()
		require.Contains(t, content, "📋 Pre-Review Summary:")
		require.Contains(t, content, "Standalone pre-review summary text.")
	})
}

func TestIndentText(t *testing.T) {
	input := "line 1\nline 2\n\nline 3"
	got := indentText(input, 2)
	expected := "  line 1\n  line 2\n\n  line 3"
	require.Equal(t, expected, got)
}
