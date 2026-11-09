package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseGitHubCommentURL(t *testing.T) {
	valid := []struct {
		url  string
		want commentRef
	}{
		{"https://github.com/menny/cassandra/pull/150#issuecomment-6663940577", commentRef{"menny", "cassandra", 150, 6663940577, commentKindIssue}},
		{"https://github.com/o/r/issues/7#issuecomment-5", commentRef{"o", "r", 7, 5, commentKindIssue}},
		{"https://github.com/o/r/pull/42#discussion_r456", commentRef{"o", "r", 42, 456, commentKindReview}},
		{"https://github.com/o/r/pull/42/files#r456", commentRef{"o", "r", 42, 456, commentKindReview}},
		{"https://github.com/o/r/pull/42#pullrequestreview-789", commentRef{"o", "r", 42, 789, commentKindReviewSummary}},
		{"  https://www.github.com/o/r/pull/42/#issuecomment-1  ", commentRef{"o", "r", 42, 1, commentKindIssue}},
		{"http://github.com/o/r/pull/42#issuecomment-1", commentRef{"o", "r", 42, 1, commentKindIssue}},
	}
	for _, tc := range valid {
		got, err := parseGitHubCommentURL(tc.url)
		require.NoError(t, err, tc.url)
		require.Equal(t, tc.want, *got, tc.url)
	}

	invalid := []string{
		"",
		"not a url",
		"https://evil.com/o/r/pull/1#issuecomment-1",
		"https://github.com.evil.com/o/r/pull/1#issuecomment-1",
		"https://evil.com/github.com/o/r/pull/1#issuecomment-1",
		"https://user@github.com@evil.com/o/r/pull/1#issuecomment-1",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"https://github.com/o/r/pull/1",
		"https://github.com/o/r/pull/1#",
		"https://github.com/o/r/pull/1#issuecomment-abc",
		"https://github.com/o/r/pull/1#issuecomment-0",
		"https://github.com/o/r/pull/1#issuecomment--5",
		"https://github.com/o/r/pull/x#issuecomment-1",
		"https://github.com/o/r/pull/0#issuecomment-1",
		"https://github.com/o/r/discussions/1#issuecomment-1",
		"https://github.com/o/pull/1#issuecomment-1",
		"https://github.com/#issuecomment-1",
	}
	for _, u := range invalid {
		_, err := parseGitHubCommentURL(u)
		require.Error(t, err, u)
	}
}

const issueCommentJSON = `{"id":6663940577,"html_url":"https://github.com/o/r/pull/42#issuecomment-6663940577",
"issue_url":"https://api.github.com/repos/o/r/issues/42","body":"Please read the **other** comment",
"user":{"login":"alice"},"created_at":"2025-01-02T03:04:05Z","updated_at":"2025-01-03T03:04:05Z"}`

const reviewCommentJSON = `{"id":456,"html_url":"https://github.com/o/r/pull/42#discussion_r456",
"pull_request_url":"https://api.github.com/repos/o/r/pulls/42","body":"nit: rename this","path":"core/agent.go","line":17,
"position":3,"original_position":3,"user":{"login":"bob"},"created_at":"2025-01-02T04:00:00Z","updated_at":"2025-01-02T04:00:00Z"}`

func TestGetPRCommentTool(t *testing.T) {
	ctx := context.Background()

	t.Run("single issue comment by URL", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/comments/6663940577", http.StatusOK, issueCommentJSON)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_url": "https://github.com/o/r/pull/42#issuecomment-6663940577"})
		require.NoError(t, err)
		require.Contains(t, out, "Conversation comment 6663940577 on o/r#42")
		require.Contains(t, out, "**Author**: alice")
		require.Contains(t, out, "**Created**: 2025-01-02T03:04:05Z")
		require.Contains(t, out, "**Updated**: 2025-01-03T03:04:05Z")
		require.Contains(t, out, "https://github.com/o/r/pull/42#issuecomment-6663940577")
		require.Contains(t, out, "Please read the **other** comment")
	})

	t.Run("single inline review comment by URL", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/comments/456", http.StatusOK, reviewCommentJSON)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_url": "https://github.com/o/r/pull/42#discussion_r456"})
		require.NoError(t, err)
		require.Contains(t, out, "Inline review comment 456 on o/r#42")
		require.Contains(t, out, "**Location**: core/agent.go:17")
		require.Contains(t, out, "**Outdated**: false")
		require.Contains(t, out, "nit: rename this")
	})

	t.Run("review summary by URL", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42/reviews/789", http.StatusOK,
			`{"id":789,"state":"CHANGES_REQUESTED","body":"Needs work","user":{"login":"carol"},"submitted_at":"2025-02-01T00:00:00Z","html_url":"https://github.com/o/r/pull/42#pullrequestreview-789"}`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_url": "https://github.com/o/r/pull/42#pullrequestreview-789"})
		require.NoError(t, err)
		require.Contains(t, out, "Review 789 on o/r#42")
		require.Contains(t, out, "**State**: CHANGES_REQUESTED")
		require.Contains(t, out, "Needs work")
	})

	t.Run("plain issue comment URL", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/comments/5", http.StatusOK,
			`{"id":5,"html_url":"https://github.com/o/r/issues/7#issuecomment-5","issue_url":"https://api.github.com/repos/o/r/issues/7","body":"on an issue","user":{"login":"dan"},"created_at":"2025-01-01T00:00:00Z"}`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_url": "https://github.com/o/r/issues/7#issuecomment-5"})
		require.NoError(t, err)
		require.Contains(t, out, "on o/r#7")
		require.Contains(t, out, "on an issue")
	})

	t.Run("bare comment_id falls back from issue to review comment", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/comments/456", http.StatusOK, reviewCommentJSON)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_id": 456, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "nit: rename this")
		require.Len(t, f.requests, 2, "issue comment is tried first")
	})

	t.Run("explicit kind does not fall back", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/comments/456", http.StatusOK, reviewCommentJSON)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		_, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_id": 456, "repo": "o/r", "kind": "issue"})
		require.ErrorContains(t, err, "not found")
		require.Len(t, f.requests, 1)
	})

	t.Run("bare comment_id that exists nowhere", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		_, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_id": 99, "repo": "o/r"})
		require.ErrorContains(t, err, "comment 99 not found in o/r")
	})

	t.Run("repository is taken from the metadata file", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/meta-owner/meta-repo/issues/comments/5", http.StatusOK, `{"id":5,"body":"hello","user":{"login":"x"}}`)
		f.install()
		metaFile := t.TempDir() + "/meta.json"
		require.NoError(t, writeTestFile(metaFile, `{"repo_full_name":"meta-owner/meta-repo","pr_number":3}`))
		r := NewRegistry()
		registerGetPRCommentTool(r, "", metaFile)

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_id": 5})
		require.NoError(t, err)
		require.Contains(t, out, "hello")
	})

	t.Run("argument validation", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		cases := []struct {
			name string
			args map[string]any
			want string
		}{
			{"url and id", map[string]any{"comment_url": "https://github.com/o/r/pull/1#issuecomment-1", "comment_id": 1}, "either comment_url or comment_id"},
			{"bad kind", map[string]any{"comment_id": 1, "kind": "bogus"}, "invalid kind"},
			{"negative id", map[string]any{"comment_id": -1}, "greater than 0"},
			{"foreign host", map[string]any{"comment_url": "https://evil.example/o/r/pull/1#issuecomment-1"}, "only github.com URLs"},
			{"repo mismatch", map[string]any{"comment_url": "https://github.com/o/r/pull/1#issuecomment-1", "repo": "other/r"}, "belongs to o/r"},
			{"pr mismatch", map[string]any{"comment_url": "https://github.com/o/r/pull/1#issuecomment-1", "pr_number": 2}, "refers to #1"},
			{"bad repo slug", map[string]any{"comment_id": 1, "repo": "not-a-slug"}, "owner/repo"},
		}
		for _, tc := range cases {
			_, err := callTool(ctx, t, r, "get_pr_comment", tc.args)
			require.ErrorContains(t, err, tc.want, tc.name)
		}
		require.Empty(t, f.requests, "validation failures must not reach the network")
	})

	t.Run("malformed arguments", func(t *testing.T) {
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")
		_, err := r.HandleCall(ctx, llmToolCall("get_pr_comment", `{"comment_id": "abc"}`))
		require.ErrorContains(t, err, "unmarshaling get_pr_comment arguments")
	})

	t.Run("missing token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_id": 1, "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("forbidden is explained", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/comments/1", http.StatusForbidden, `{"message":"Resource not accessible"}`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"comment_id": 1, "repo": "o/r"})
		require.ErrorContains(t, err, "forbidden")
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/comments/1", http.StatusOK, issueCommentJSON)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "get_pr_comment", map[string]any{"comment_id": 1, "repo": "o/r"})
		require.Error(t, err)
		_, err = callTool(cctx, t, r, "get_pr_comment", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.Error(t, err)
	})
}

func TestGetPRCommentToolListMode(t *testing.T) {
	ctx := context.Background()

	t.Run("lists conversation and inline comments chronologically with pagination", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.handle("GET /repos/o/r/issues/42/comments", func(req *http.Request) *http.Response {
			if req.URL.Query().Get("page") == "2" {
				return jsonResp(http.StatusOK, `[{"id":3,"body":"third\nline","user":{"login":"carol"},"created_at":"2025-01-03T00:00:00Z"}]`, nil)
			}
			return jsonResp(http.StatusOK, `[{"id":1,"body":"first","user":{"login":"alice"},"created_at":"2025-01-01T00:00:00Z"}]`,
				map[string]string{"Link": `<https://api.github.com/repos/o/r/issues/42/comments?page=2&per_page=100>; rel="next"`})
		})
		f.json("GET /repos/o/r/pulls/42/comments", http.StatusOK,
			`[{"id":2,"body":"inline","path":"a.go","line":9,"position":1,"original_position":1,"user":{"login":"bob"},"created_at":"2025-01-02T00:00:00Z"},
			  {"id":4,"body":"stale","path":"b.go","original_line":4,"original_position":2,"user":{"login":"bob"},"created_at":"2025-01-04T00:00:00Z"}]`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Comments on o/r#42 (2 conversation, 2 inline review)")
		iFirst, iInline, iThird, iStale := strings.Index(out, "id=1 "), strings.Index(out, "id=2 "), strings.Index(out, "id=3 "), strings.Index(out, "id=4 ")
		require.True(t, 0 < iFirst && iFirst < iInline && iInline < iThird && iThird < iStale, out)
		require.Contains(t, out, "[review] id=2 bob 2025-01-02T00:00:00Z a.go:9")
		require.Contains(t, out, "b.go:4 (outdated)")
		require.Contains(t, out, "third line", "newlines collapse into one line")
	})

	t.Run("plain issues have no review comments", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/7/comments", http.StatusOK, `[{"id":1,"body":"only","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z"}]`)
		f.install() // pulls/7/comments is a 404
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"pr_number": 7, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "1 conversation, 0 inline review")
	})

	t.Run("no comments", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/7/comments", http.StatusOK, `[]`)
		f.json("GET /repos/o/r/pulls/7/comments", http.StatusOK, `[]`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"pr_number": 7, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "(no comments)")
	})

	t.Run("oversized listings keep the most recent comments", func(t *testing.T) {
		var items []string
		for i := 1; i <= 400; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"body":%q,"user":{"login":"u"},"created_at":"2025-01-01T00:%02d:%02dZ"}`,
				i, strings.Repeat("x", 300), i/60, i%60))
		}
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/7/comments", http.StatusOK, "["+strings.Join(items, ",")+"]")
		f.json("GET /repos/o/r/pulls/7/comments", http.StatusOK, `[]`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"pr_number": 7, "repo": "o/r"})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.Contains(t, out, "older comments omitted")
		require.Contains(t, out, "id=400 ")
		require.NotContains(t, out, "id=1 ")
	})

	t.Run("API failure is reported", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/7/comments", http.StatusInternalServerError, `{"message":"boom"}`)
		f.install()
		r := NewRegistry()
		registerGetPRCommentTool(r, "", "")

		_, err := callTool(ctx, t, r, "get_pr_comment", map[string]any{"pr_number": 7, "repo": "o/r"})
		require.ErrorContains(t, err, "list issue comments")
	})
}
