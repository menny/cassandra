package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func commitJSON(i int) string {
	return fmt.Sprintf(`{"sha":"%d%039d","author":{"login":"user%d"},"commit":{"message":"subject %d\n\nbody %d","author":{"name":"Name %d","date":"2025-03-%02dT10:00:00Z"}}}`, i, 0, i, i, i, i, i%28+1)
}

func TestListPRCommitsTool(t *testing.T) {
	ctx := context.Background()

	t.Run("lists commits", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42/commits", http.StatusOK, "["+commitJSON(1)+","+commitJSON(2)+"]")
		f.install()
		r := NewRegistry()
		registerListPRCommitsTool(r, "", "")

		out, err := callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Commits of o/r#42 (2)")
		require.Contains(t, out, "- 100000000000 2025-03-02 user1: subject 1")
		require.Contains(t, out, "user2: subject 2")
		require.NotContains(t, out, "body 1", "only the subject line is listed")
		require.NotContains(t, out, "limited")
	})

	t.Run("falls back to the git author name", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42/commits", http.StatusOK,
			`[{"sha":"abc","commit":{"message":"m","author":{"name":"Plain Name","date":"2025-01-01T00:00:00Z"}}}]`)
		f.install()
		r := NewRegistry()
		registerListPRCommitsTool(r, "", "")
		out, err := callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Plain Name: m")
	})

	t.Run("follows pagination and honors max_commits", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.handle("GET /repos/o/r/pulls/42/commits", func(req *http.Request) *http.Response {
			if req.URL.Query().Get("page") == "2" {
				return jsonResp(http.StatusOK, "["+commitJSON(3)+","+commitJSON(4)+"]", nil)
			}
			return jsonResp(http.StatusOK, "["+commitJSON(1)+","+commitJSON(2)+"]", map[string]string{
				"Link": `<https://api.github.com/repos/o/r/pulls/42/commits?page=2&per_page=100>; rel="next"`,
			})
		})
		f.install()
		r := NewRegistry()
		registerListPRCommitsTool(r, "", "")

		out, err := callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Commits of o/r#42 (4)")
		require.Contains(t, out, "user4")

		out, err = callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 42, "repo": "o/r", "max_commits": 3})
		require.NoError(t, err)
		require.Contains(t, out, "Commits of o/r#42 (3)")
		require.NotContains(t, out, "user4")
		require.Contains(t, out, "list limited")
	})

	t.Run("uses pr_number from the metadata file", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/meta/repo/pulls/77/commits", http.StatusOK, "["+commitJSON(1)+"]")
		f.install()
		metaFile := t.TempDir() + "/meta.json"
		require.NoError(t, writeTestFile(metaFile, `{"repo_full_name":"meta/repo","pr_number":77}`))
		r := NewRegistry()
		registerListPRCommitsTool(r, "", metaFile)
		out, err := callTool(ctx, t, r, "list_pr_commits", map[string]any{})
		require.NoError(t, err)
		require.Contains(t, out, "meta/repo#77")
	})

	t.Run("output is capped", func(t *testing.T) {
		var items []string
		for i := 0; i < 100; i++ {
			items = append(items, fmt.Sprintf(`{"sha":"%040d","commit":{"message":%q,"author":{"name":"n","date":"2025-01-01T00:00:00Z"}}}`, i, strings.Repeat("s", 2000)))
		}
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/1/commits", http.StatusOK, "["+strings.Join(items, ",")+"]")
		f.install()
		r := NewRegistry()
		registerListPRCommitsTool(r, "", "")
		out, err := callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 1, "repo": "o/r"})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.True(t, strings.HasSuffix(out, truncationNotice))
	})

	t.Run("errors", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerListPRCommitsTool(r, "", "")

		_, err := callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": -1, "repo": "o/r"})
		require.ErrorContains(t, err, "pr_number must be greater than 0")
		_, err = callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 5, "repo": "o/r"})
		require.ErrorContains(t, err, "not found")
		_, err = r.HandleCall(ctx, llmToolCall("list_pr_commits", `{"max_commits": "all"}`))
		require.ErrorContains(t, err, "unmarshaling list_pr_commits arguments")

		t.Setenv("GITHUB_TOKEN", "")
		_, err = callTool(ctx, t, r, "list_pr_commits", map[string]any{"pr_number": 5, "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/1/commits", http.StatusOK, "["+commitJSON(1)+"]")
		f.install()
		r := NewRegistry()
		registerListPRCommitsTool(r, "", "")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "list_pr_commits", map[string]any{"pr_number": 1, "repo": "o/r"})
		require.Error(t, err)
	})
}
