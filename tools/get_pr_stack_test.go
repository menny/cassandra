package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func stackPR(num int, title, headRef, baseRef, headRepo string) string {
	return fmt.Sprintf(`{"number":%d,"title":%q,"head":{"ref":%q,"sha":"sha%d","repo":{"full_name":%q}},"base":{"ref":%q}}`,
		num, title, headRef, num, headRepo, baseRef)
}

// routePullList answers GET /repos/o/r/pulls by head or base filter. Unlisted filters yield [].
func routePullList(f *fakeGitHub, byHead, byBase map[string][]string) {
	f.handle("GET /repos/o/r/pulls", func(req *http.Request) *http.Response {
		q := req.URL.Query()
		var prs []string
		if h := q.Get("head"); h != "" {
			prs = byHead[strings.TrimPrefix(h, "o:")]
		} else if b := q.Get("base"); b != "" {
			prs = byBase[b]
		}
		return jsonResp(http.StatusOK, "["+strings.Join(prs, ",")+"]", nil)
	})
}

func TestGetPRStackTool(t *testing.T) {
	ctx := context.Background()
	newTool := func(t *testing.T, f *fakeGitHub) *Registry {
		f.install()
		r := NewRegistry()
		registerGetPRStackTool(r, "", "")
		return r
	}

	t.Run("uses GitHub's Stacks API when available", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/stacks", http.StatusOK, `[{"id":1,"number":7,"pull_requests":[{"number":101},{"number":102}]}]`)
		f.json("GET /repos/o/r/stacks/7", http.StatusOK,
			`{"id":1,"number":7,"pull_requests":[
			  {"number":101,"title":"Base Model","head":{"ref":"model","sha":"sha1"},"base":{"ref":"main"}},
			  {"number":102,"title":"API Layer","head":{"ref":"api","sha":"sha2"},"base":{"ref":"model"}}]}`)
		r := newTool(t, f)

		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 102, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "PR stack for o/r#102 (2 PRs, bottom to top; source: GitHub Stacks API)")
		require.Contains(t, out, "- #101: Base Model (branch: model, base: main, head: sha1)\n")
		require.Contains(t, out, "- #102: API Layer (branch: api, base: model, head: sha2) <- (this PR)")
		for _, req := range f.requests {
			require.NotContains(t, req, "/pulls", "no inference needed")
		}
	})

	t.Run("infers the stack from branch names in both directions", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42", http.StatusOK, stackPR(42, "Middle", "api", "model", "o/r"))
		routePullList(f,
			map[string][]string{"model": {stackPR(41, "Bottom", "model", "main", "o/r")}},
			map[string][]string{"api": {stackPR(43, "Top", "ui", "api", "o/r")}})
		r := newTool(t, f)

		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "3 PRs, bottom to top; source: inferred from base/head branches")
		i41, i42, i43 := strings.Index(out, "- #41:"), strings.Index(out, "- #42:"), strings.Index(out, "- #43:")
		require.True(t, 0 < i41 && i41 < i42 && i42 < i43, out)
		require.Contains(t, out, "- #42: Middle (branch: api, base: model, head: sha42) <- (this PR)")
	})

	t.Run("a PR alone is not a stack", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42", http.StatusOK, stackPR(42, "Solo", "feature", "main", "o/r"))
		routePullList(f, nil, nil)
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "PR o/r#42 is not part of a stack")
	})

	t.Run("ignores same-named branches in forks and reports forks in the stack", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42", http.StatusOK, stackPR(42, "Mine", "api", "model", "o/r"))
		routePullList(f,
			map[string][]string{"model": {stackPR(99, "Fork PR", "model", "main", "stranger/r")}},
			map[string][]string{"api": {stackPR(45, "Child B", "ui-b", "api", "o/r"), stackPR(43, "Child A", "ui-a", "api", "o/r")}})
		r := newTool(t, f)

		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.NotContains(t, out, "#99")
		require.Contains(t, out, "- #43: Child A")
		require.NotContains(t, out, "- #45:")
		require.Contains(t, out, `Note: Multiple open PRs are based on branch "api" (#43, #45); following #43. The stack forks here.`)
	})

	t.Run("cycles terminate", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/1", http.StatusOK, stackPR(1, "A", "a", "b", "o/r"))
		routePullList(f,
			map[string][]string{"b": {stackPR(2, "B", "b", "a", "o/r")}, "a": {stackPR(1, "A", "a", "b", "o/r")}},
			map[string][]string{"a": {stackPR(2, "B", "b", "a", "o/r")}, "b": {stackPR(1, "A", "a", "b", "o/r")}})
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 1, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "2 PRs")
		require.Less(t, len(f.requests), 12)
	})

	t.Run("walks are bounded", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/1000", http.StatusOK, stackPR(1000, "Start", "n0", "main", "o/r"))
		f.handle("GET /repos/o/r/pulls", func(req *http.Request) *http.Response {
			var n int
			if b := req.URL.Query().Get("base"); b != "" {
				_, _ = fmt.Sscanf(b, "n%d", &n)
				return jsonResp(http.StatusOK, "["+stackPR(1001+n, "Child", fmt.Sprintf("n%d", n+1), b, "o/r")+"]", nil)
			}
			return jsonResp(http.StatusOK, "[]", nil)
		})
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 1000, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, fmt.Sprintf("Stopped looking for child PRs after %d steps", maxStackWalk))
		require.LessOrEqual(t, len(f.requests), 2+maxStackWalk+2, "requests must stay bounded")
	})

	t.Run("falls back with a note when the Stacks API fails", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/stacks", http.StatusForbidden, `{"message":"nope"}`)
		f.json("GET /repos/o/r/pulls/42", http.StatusOK, stackPR(42, "Mid", "api", "model", "o/r"))
		routePullList(f, map[string][]string{"model": {stackPR(41, "Bottom", "model", "main", "o/r")}}, nil)
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Stacks API failed")
		require.Contains(t, out, "- #41:")
	})

	t.Run("pr_number comes from the metadata file", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/meta/repo/pulls/77", http.StatusOK, stackPR(77, "Meta PR", "x", "main", "meta/repo"))
		f.handle("GET /repos/meta/repo/pulls", func(*http.Request) *http.Response { return jsonResp(http.StatusOK, "[]", nil) })
		f.install()
		metaFile := t.TempDir() + "/meta.json"
		require.NoError(t, writeTestFile(metaFile, `{"repo_full_name":"meta/repo","pr_number":77}`))
		r := NewRegistry()
		registerGetPRStackTool(r, "", metaFile)
		out, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{})
		require.NoError(t, err)
		require.Contains(t, out, "meta/repo#77 is not part of a stack")
	})

	t.Run("errors", func(t *testing.T) {
		f := newFakeGitHub(t)
		r := newTool(t, f)
		_, err := callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 5, "repo": "o/r"})
		require.ErrorContains(t, err, "get PR #5")
		_, err = callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": -1, "repo": "o/r"})
		require.ErrorContains(t, err, "greater than 0")
		_, err = r.HandleCall(ctx, llmToolCall("get_pr_stack", `{"pr_number": "x"}`))
		require.ErrorContains(t, err, "unmarshaling get_pr_stack arguments")

		f2 := newFakeGitHub(t)
		f2.json("GET /repos/o/r/pulls/42", http.StatusOK, stackPR(42, "P", "api", "model", "o/r"))
		f2.json("GET /repos/o/r/pulls", http.StatusInternalServerError, `{"message":"boom"}`)
		r2 := newTool(t, f2)
		_, err = callTool(ctx, t, r2, "get_pr_stack", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.ErrorContains(t, err, `list pull requests with head branch "model"`)

		t.Setenv("GITHUB_TOKEN", "")
		_, err = callTool(ctx, t, r, "get_pr_stack", map[string]any{"pr_number": 5, "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("InferPRStack validates its arguments", func(t *testing.T) {
		_, _, err := InferPRStack(ctx, nil, "o", "r", 1)
		require.Error(t, err)
		f := newFakeGitHub(t)
		_, _, err = InferPRStack(ctx, f.client(), "", "r", 1)
		require.Error(t, err)
		_, _, err = InferPRStack(ctx, f.client(), "o", "r", 0)
		require.Error(t, err)
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42", http.StatusOK, stackPR(42, "P", "api", "model", "o/r"))
		r := newTool(t, f)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "get_pr_stack", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.Error(t, err)
	})
}
