package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// commentPages serves count issue comments over several pages of the given size, newest ID last.
func commentPages(f *fakeGitHub, route string, count, perPage int, itemFn func(i int) string) {
	f.handle("GET "+route, func(req *http.Request) *http.Response {
		page := 1
		_, _ = fmt.Sscanf(req.URL.Query().Get("page"), "%d", &page)
		start := (page - 1) * perPage
		end := start + perPage
		if end > count {
			end = count
		}
		var items []string
		for i := start; i < end; i++ {
			items = append(items, itemFn(i))
		}
		header := map[string]string{}
		if end < count {
			header["Link"] = fmt.Sprintf(`<https://api.github.com%s?page=%d&per_page=%d>; rel="next"`, route, page+1, perPage)
		}
		return jsonResp(http.StatusOK, "["+strings.Join(items, ",")+"]", header)
	})
}

func issueCommentItem(i int) string {
	return fmt.Sprintf(`{"id":%d,"body":"issue comment %d","user":{"login":"u%d"},"created_at":"2025-01-01T00:%02d:%02dZ","issue_url":"https://api.github.com/repos/o/r/issues/5"}`,
		i+1, i, i, i/60, i%60)
}

func TestFetchPRComments(t *testing.T) {
	ctx := context.Background()

	t.Run("maps fields and merges both kinds chronologically", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/5/comments", http.StatusOK,
			`[{"id":1,"body":"conv","user":{"login":"alice"},"created_at":"2025-01-03T00:00:00Z","updated_at":"2025-01-04T00:00:00Z","html_url":"https://github.com/o/r/pull/5#issuecomment-1","issue_url":"https://api.github.com/repos/o/r/issues/5"}]`)
		f.json("GET /repos/o/r/pulls/5/comments", http.StatusOK,
			`[{"id":2,"body":"inline","path":"a.go","line":9,"start_line":7,"original_line":3,"position":4,"original_position":4,"user":{"login":"bob"},"created_at":"2025-01-02T00:00:00Z","pull_request_url":"https://api.github.com/repos/o/r/pulls/5"},
			  {"id":3,"body":"stale","path":"b.go","original_line":12,"original_position":2,"user":{"login":"bob"},"created_at":"2025-01-05T00:00:00Z"}]`)
		client := f.client()

		got, truncated, err := FetchPRComments(ctx, client, "o", "r", 5, FetchPRCommentsOptions{})
		require.NoError(t, err)
		require.False(t, truncated)
		require.Len(t, got, 3)

		require.Equal(t, int64(2), got[0].ID, "oldest first")
		require.Equal(t, PRCommentKindReview, got[0].Kind)
		require.Equal(t, PRCommentRecord{
			Kind: PRCommentKindReview, ID: 2, Number: 5, Author: "bob", Body: "inline",
			Created: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Path: "a.go", Line: 9, StartLine: 7, OriginalLine: 3,
		}, got[0])

		require.Equal(t, PRCommentKindIssue, got[1].Kind)
		require.Equal(t, 5, got[1].Number)
		require.Equal(t, "alice", got[1].Author)
		require.Equal(t, time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), got[1].Updated)
		require.Equal(t, "https://github.com/o/r/pull/5#issuecomment-1", got[1].URL)

		require.True(t, got[2].Outdated, "no current position but an original one")
		require.Zero(t, got[2].Line)
		require.Equal(t, 12, got[2].DisplayLine(), "falls back to the original line")
		require.Equal(t, 9, got[0].DisplayLine())
	})

	t.Run("reads every page by default", func(t *testing.T) {
		f := newFakeGitHub(t)
		commentPages(f, "/repos/o/r/issues/5/comments", 250, 100, issueCommentItem)
		f.json("GET /repos/o/r/pulls/5/comments", http.StatusOK, `[]`)

		got, truncated, err := FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{})
		require.NoError(t, err)
		require.False(t, truncated)
		require.Len(t, got, 250)
	})

	t.Run("MaxPagesPerKind caps each kind and reports truncation", func(t *testing.T) {
		f := newFakeGitHub(t)
		commentPages(f, "/repos/o/r/issues/5/comments", 250, 100, issueCommentItem)
		f.json("GET /repos/o/r/pulls/5/comments", http.StatusOK, `[]`)

		got, truncated, err := FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{MaxPagesPerKind: 2})
		require.NoError(t, err)
		require.True(t, truncated)
		require.Len(t, got, 200)

		got, truncated, err = FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{MaxPagesPerKind: 3})
		require.NoError(t, err)
		require.False(t, truncated, "the cap is exactly enough")
		require.Len(t, got, 250)
	})

	t.Run("review comment errors are fatal unless 404s are ignored", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/5/comments", http.StatusOK, `[{"id":1,"body":"x","user":{"login":"a"},"created_at":"2025-01-01T00:00:00Z"}]`)
		// pulls/5/comments is a 404, as for a plain issue.

		_, _, err := FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{})
		require.ErrorContains(t, err, "failed to list review comments")

		got, _, err := FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{IgnoreMissingReviewComments: true})
		require.NoError(t, err)
		require.Len(t, got, 1)

		f.json("GET /repos/o/r/pulls/5/comments", http.StatusInternalServerError, `{"message":"boom"}`)
		_, _, err = FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{IgnoreMissingReviewComments: true})
		require.ErrorContains(t, err, "failed to list review comments", "only 404s are tolerated")
	})

	t.Run("issue comment errors are fatal", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/5/comments", http.StatusForbidden, `{"message":"no"}`)
		_, _, err := FetchPRComments(ctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{IgnoreMissingReviewComments: true})
		require.ErrorContains(t, err, "failed to list issue comments")
		require.ErrorContains(t, err, "forbidden")
	})

	t.Run("argument validation", func(t *testing.T) {
		f := newFakeGitHub(t)
		_, _, err := FetchPRComments(ctx, nil, "o", "r", 5, FetchPRCommentsOptions{})
		require.Error(t, err)
		for _, bad := range []struct {
			owner, repo string
			n           int
		}{{"", "r", 1}, {"o", "", 1}, {"o", "r", 0}, {"o", "r", -3}} {
			_, _, err = FetchPRComments(ctx, f.client(), bad.owner, bad.repo, bad.n, FetchPRCommentsOptions{})
			require.Error(t, err)
		}
		require.Empty(t, f.requests)
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/issues/5/comments", http.StatusOK, `[]`)
		f.json("GET /repos/o/r/pulls/5/comments", http.StatusOK, `[]`)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, _, err := FetchPRComments(cctx, f.client(), "o", "r", 5, FetchPRCommentsOptions{})
		require.Error(t, err)
	})
}
