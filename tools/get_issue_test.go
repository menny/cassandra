package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const issueJSON = `{"number":123,"title":"Crash on empty diff","state":"closed","state_reason":"completed",
"user":{"login":"alice"},"labels":[{"name":"bug"},{"name":"p1"}],"assignees":[{"login":"bob"}],
"milestone":{"title":"v1.2"},"comments":3,"html_url":"https://github.com/o/r/issues/123",
"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-05T00:00:00Z","closed_at":"2025-01-05T00:00:00Z",
"body":"Steps to reproduce..."}`

func TestGetIssueTool(t *testing.T) {
	ctx := context.Background()

	t.Run("returns issue details", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/123", http.StatusOK, issueJSON)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 123, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "### Issue o/r#123: Crash on empty diff")
		require.Contains(t, out, "**State**: closed (completed)")
		require.Contains(t, out, "**Labels**: bug, p1")
		require.Contains(t, out, "**Assignees**: bob")
		require.Contains(t, out, "**Milestone**: v1.2")
		require.Contains(t, out, "Steps to reproduce...")
		require.NotContains(t, out, "Comments (showing", "comments are opt-in")
		require.Len(t, f.requests, 1)
	})

	t.Run("pull requests are labelled as such", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/9", http.StatusOK,
			`{"number":9,"title":"A PR","state":"open","pull_request":{"url":"https://api.github.com/repos/o/r/pulls/9"},"user":{"login":"a"}}`)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 9, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "### Pull request o/r#9")
		require.Contains(t, out, "(no description)")
	})

	t.Run("includes only the most recent comments", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/123", http.StatusOK, issueJSON)
		f.json("GET /repos/o/r/issues/123/comments", http.StatusOK,
			`[{"id":1,"body":"c-one","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z"},
			  {"id":2,"body":"c-two","user":{"login":"b"},"created_at":"2025-01-02T00:00:00Z"},
			  {"id":3,"body":"c-three","user":{"login":"c"},"created_at":"2025-01-03T00:00:00Z"}]`)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 123, "repo": "o/r", "include_comments": true, "max_comments": 2})
		require.NoError(t, err)
		require.Contains(t, out, "Comments (showing last 2 of 3)")
		require.NotContains(t, out, "c-one")
		require.Contains(t, out, "c-two")
		require.Contains(t, out, "c-three")
	})

	t.Run("huge bodies are truncated", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/1", http.StatusOK, `{"number":1,"title":"big","state":"open","user":{"login":"a"},"body":"`+strings.Repeat("y", 100000)+`"}`)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 1, "repo": "o/r"})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.True(t, strings.HasSuffix(out, truncationNotice))
	})

	t.Run("argument validation", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")

		_, err := callTool(ctx, t, r, "get_issue", map[string]any{"repo": "o/r"})
		require.ErrorContains(t, err, "issue_number is required")
		_, err = callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": -4, "repo": "o/r"})
		require.ErrorContains(t, err, "issue_number is required")
		_, err = callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 1, "repo": "bad"})
		require.ErrorContains(t, err, "owner/repo")
		_, err = r.HandleCall(ctx, llmToolCall("get_issue", `{"issue_number": "x"}`))
		require.ErrorContains(t, err, "unmarshaling get_issue arguments")
		require.Empty(t, f.requests)
	})

	t.Run("not found and missing token", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 404, "repo": "o/r"})
		require.ErrorContains(t, err, "not found")

		t.Setenv("GITHUB_TOKEN", "")
		_, err = callTool(ctx, t, r, "get_issue", map[string]any{"issue_number": 1, "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/123", http.StatusOK, issueJSON)
		f.install()
		r := NewRegistry()
		registerGetIssueTool(r, "", "")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "get_issue", map[string]any{"issue_number": 123, "repo": "o/r"})
		require.Error(t, err)
	})
}
