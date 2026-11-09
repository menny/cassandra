package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const checkRunsJSON = `{"total_count":3,"check_runs":[
 {"id":11,"name":"build","status":"completed","conclusion":"success","html_url":"https://github.com/o/r/runs/11","app":{"name":"GitHub Actions"},"output":{"title":"Build ok","summary":"all good"}},
 {"id":12,"name":"test","status":"completed","conclusion":"failure","html_url":"https://github.com/o/r/runs/12","app":{"name":"GitHub Actions"},"output":{"title":"2 failed","summary":"TestX failed\nTestY failed"}},
 {"id":13,"name":"lint","status":"in_progress"}]}`

const combinedStatusJSON = `{"state":"failure","statuses":[
 {"context":"ci/legacy","state":"failure","description":"it broke","target_url":"https://ci.example.com/1"},
 {"context":"ci/ok","state":"success"}]}`

func TestGetCheckRunsTool(t *testing.T) {
	ctx := context.Background()

	t.Run("by ref lists failures first with commit statuses", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/commits/abc123/check-runs", http.StatusOK, checkRunsJSON)
		f.json("GET /repos/o/r/commits/abc123/status", http.StatusOK, combinedStatusJSON)
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Check runs for o/r@abc123")
		require.Contains(t, out, "3 check runs: 1 failed, 1 not finished, 1 passed or neutral")
		iTest, iLint, iBuild := strings.Index(out, "] test "), strings.Index(out, "] lint "), strings.Index(out, "] build ")
		require.True(t, 0 < iTest && iTest < iLint && iLint < iBuild, out)
		require.Contains(t, out, "[failure] test (id=12, app=GitHub Actions) - 2 failed: TestX failed TestY failed <https://github.com/o/r/runs/12>")
		require.Contains(t, out, "[in_progress] lint (id=13)")
		require.Contains(t, out, "Commit statuses: failure")
		require.Contains(t, out, "[failure] ci/legacy - it broke")
		require.Contains(t, out, "job_id accepted by get_workflow_run_logs")
	})

	t.Run("only_failures hides passing runs and statuses", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/commits/abc123/check-runs", http.StatusOK, checkRunsJSON)
		f.json("GET /repos/o/r/commits/abc123/status", http.StatusOK, combinedStatusJSON)
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r", "only_failures": true})
		require.NoError(t, err)
		require.Contains(t, out, "] test ")
		require.NotContains(t, out, "] build ")
		require.NotContains(t, out, "] lint ")
		require.NotContains(t, out, "ci/ok")
		require.Contains(t, out, "1 failed", "the summary still counts everything")
	})

	t.Run("by pull request uses the head commit", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/pulls/42", http.StatusOK, `{"number":42,"head":{"sha":"deadbeefdeadbeefdeadbeef"}}`)
		f.json("GET /repos/o/r/commits/deadbeefdeadbeefdeadbeef/check-runs", http.StatusOK, `{"total_count":0,"check_runs":[]}`)
		f.json("GET /repos/o/r/commits/deadbeefdeadbeefdeadbeef/status", http.StatusOK, `{"state":"pending","statuses":[]}`)
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"pr_number": 42, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "o/r@deadbeefdead (PR #42 head)")
		require.Contains(t, out, "(no check runs)")
	})

	t.Run("pagination", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.handle("GET /repos/o/r/commits/abc123/check-runs", func(req *http.Request) *http.Response {
			if req.URL.Query().Get("page") == "2" {
				return jsonResp(http.StatusOK, `{"total_count":2,"check_runs":[{"id":2,"name":"second","status":"completed","conclusion":"success"}]}`, nil)
			}
			return jsonResp(http.StatusOK, `{"total_count":2,"check_runs":[{"id":1,"name":"first","status":"completed","conclusion":"success"}]}`,
				map[string]string{"Link": `<https://api.github.com/repos/o/r/commits/abc123/check-runs?page=2&per_page=100>; rel="next"`})
		})
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "2 check runs")
		require.Contains(t, out, "] second ")
	})

	t.Run("unavailable commit statuses do not fail the call", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/commits/abc123/check-runs", http.StatusOK, checkRunsJSON)
		f.install() // /status is a 404
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "Commit statuses unavailable")
		require.Contains(t, out, "] test ")
	})

	t.Run("oversized output is truncated", func(t *testing.T) {
		var items []string
		for i := 0; i < 100; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"check-%d","status":"completed","conclusion":"failure","output":{"title":"t","summary":%q}}`, i, i, strings.Repeat("z", 1000)))
		}
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/commits/abc123/check-runs", http.StatusOK, `{"total_count":100,"check_runs":[`+strings.Join(items, ",")+`]}`)
		f.json("GET /repos/o/r/commits/abc123/status", http.StatusOK, `{"state":"success","statuses":[]}`)
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
	})

	t.Run("errors", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")

		_, err := callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.ErrorContains(t, err, "not found")
		_, err = callTool(ctx, t, r, "get_check_runs", map[string]any{"pr_number": 7, "repo": "o/r"})
		require.ErrorContains(t, err, "get PR #7")
		_, err = callTool(ctx, t, r, "get_check_runs", map[string]any{"pr_number": -2, "repo": "o/r"})
		require.ErrorContains(t, err, "pr_number must be greater than 0")
		for _, ref := range []string{"--bad", "a b", "a?b=c", "a#b", "a/../../orgs/x", "a//b", "main/", "main..HEAD", "@{u}", "x@{1}"} {
			_, err = callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": ref, "repo": "o/r"})
			require.ErrorContains(t, err, "invalid ref", ref)
		}
		_, err = r.HandleCall(ctx, llmToolCall("get_check_runs", `{"only_failures": "yes"}`))
		require.ErrorContains(t, err, "unmarshaling get_check_runs arguments")

		t.Setenv("GITHUB_TOKEN", "")
		_, err = callTool(ctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/commits/abc123/check-runs", http.StatusOK, checkRunsJSON)
		f.install()
		r := NewRegistry()
		registerGetCheckRunsTool(r, "", "")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "get_check_runs", map[string]any{"ref": "abc123", "repo": "o/r"})
		require.Error(t, err)
	})
}
