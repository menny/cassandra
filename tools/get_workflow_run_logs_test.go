package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const signedLogURL = "https://logs.example.com/blob/job.txt?sig=SUPERSECRETSIGNATURE"

// installLogServer replaces the log download client with a fake transport for the test.
func installLogServer(t *testing.T, fn func(req *http.Request) *http.Response) *[]string {
	t.Helper()
	var urls []string
	orig := workflowLogHTTPClient
	t.Cleanup(func() { workflowLogHTTPClient = orig })
	workflowLogHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		urls = append(urls, req.URL.String())
		return fn(req), nil
	})}
	return &urls
}

func textResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body))}
}

func numberedLog(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "2025-01-01T00:00:00.000Z step output line %d\n", i)
	}
	return sb.String()
}

func routeJobLogs(f *fakeGitHub, jobID int, location string) {
	f.handle(fmt.Sprintf("GET /repos/o/r/actions/jobs/%d/logs", jobID), func(*http.Request) *http.Response {
		return jsonResp(http.StatusFound, "", map[string]string{"Location": location})
	})
}

func TestTailLines(t *testing.T) {
	t.Run("keeps only the last n lines", func(t *testing.T) {
		lines, total, err := tailLines(strings.NewReader(numberedLog(1000)), 3)
		require.NoError(t, err)
		require.Equal(t, 1000, total)
		require.Equal(t, 3, len(lines))
		require.Contains(t, lines[0], "line 998")
		require.Contains(t, lines[2], "line 1000")
	})

	t.Run("fewer lines than n, no trailing newline", func(t *testing.T) {
		lines, total, err := tailLines(strings.NewReader("a\nb"), 10)
		require.NoError(t, err)
		require.Equal(t, 2, total)
		require.Equal(t, []string{"a", "b"}, lines)
	})

	t.Run("empty input", func(t *testing.T) {
		lines, total, err := tailLines(strings.NewReader(""), 5)
		require.NoError(t, err)
		require.Zero(t, total)
		require.Empty(t, lines)
	})

	t.Run("strips BOM, CRLF and ANSI escapes", func(t *testing.T) {
		lines, _, err := tailLines(strings.NewReader("\ufeffstart\r\n\x1b[31;1mred\x1b[0m text\r\n"), 5)
		require.NoError(t, err)
		require.Equal(t, []string{"start", "red text"}, lines)
	})

	t.Run("cuts very long lines", func(t *testing.T) {
		lines, total, err := tailLines(strings.NewReader(strings.Repeat("x", 100000)+"\nnext\n"), 5)
		require.NoError(t, err)
		require.Equal(t, 2, total)
		require.LessOrEqual(t, len(lines[0]), maxLogLineBytes+len(" ...(line cut)"))
		require.True(t, strings.HasSuffix(lines[0], "...(line cut)"))
		require.Equal(t, "next", lines[1])
	})

	t.Run("memory stays bounded across compactions", func(t *testing.T) {
		lines, total, err := tailLines(strings.NewReader(numberedLog(5000)), 7)
		require.NoError(t, err)
		require.Equal(t, 5000, total)
		require.Equal(t, 7, len(lines))
		require.Contains(t, lines[6], "line 5000")
	})
}

func TestValidateLogURL(t *testing.T) {
	for _, ok := range []string{"https://logs.example.com/a?sig=1", "https://productionresultssa1.blob.core.windows.net/x"} {
		u, err := url.Parse(ok)
		require.NoError(t, err)
		require.NoError(t, validateLogURL(u), ok)
	}
	for _, bad := range []string{
		"http://logs.example.com/a", "file:///etc/passwd", "https://127.0.0.1/a", "https://[::1]/a",
		"https://169.254.169.254/latest", "https://localhost/a", "https://svc.localhost/a", "ftp://logs.example.com/a", "/relative/path",
	} {
		u, err := url.Parse(bad)
		require.NoError(t, err)
		require.Error(t, validateLogURL(u), bad)
	}
	require.Error(t, validateLogURL(nil))
}

func TestGetWorkflowRunLogsToolSingleJob(t *testing.T) {
	ctx := context.Background()

	t.Run("returns the tail of a job log without leaking the signed URL", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/actions/jobs/55", http.StatusOK, `{"id":55,"name":"unit tests","conclusion":"failure"}`)
		routeJobLogs(f, 55, signedLogURL)
		f.install()
		urls := installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, numberedLog(500)) })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 55, "repo": "o/r", "tail_lines": 3})
		require.NoError(t, err)
		require.Equal(t, []string{signedLogURL}, *urls)
		require.Contains(t, out, "### unit tests (job 55), failure")
		require.Contains(t, out, "showing the last 3 of 500 lines in total")
		require.Contains(t, out, "line 500")
		require.Contains(t, out, "line 498")
		require.NotContains(t, out, "line 497")
		require.NotContains(t, out, "SUPERSECRET")
	})

	t.Run("works without job metadata and caps tail_lines", func(t *testing.T) {
		f := newFakeGitHub(t)
		routeJobLogs(f, 56, signedLogURL)
		f.install()
		installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, numberedLog(5000)) })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 56, "repo": "o/r", "tail_lines": 100000})
		require.NoError(t, err)
		require.Contains(t, out, "### job 56")
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.Contains(t, out, "line 5000", "the end of the log must survive size trimming")
		require.NotContains(t, out, "line 1 \n")
	})

	t.Run("huge logs stop at the download cap", func(t *testing.T) {
		orig := maxLogDownloadBytes
		maxLogDownloadBytes = 2000
		t.Cleanup(func() { maxLogDownloadBytes = orig })
		f := newFakeGitHub(t)
		routeJobLogs(f, 57, signedLogURL)
		f.install()
		installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, numberedLog(5000)) })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 57, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "log exceeds")
		require.NotContains(t, out, "line 5000")
	})

	t.Run("unavailable logs are reported inline", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install() // logs endpoint is a 404 (e.g. expired)
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 58, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "logs unavailable")
		require.Contains(t, out, "not found")
	})

	t.Run("download failures never expose the signature", func(t *testing.T) {
		f := newFakeGitHub(t)
		routeJobLogs(f, 59, signedLogURL)
		f.install()
		installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusForbidden, "denied") })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 59, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "logs unavailable: downloading logs failed: unexpected status 403 Forbidden")
		require.NotContains(t, out, "SUPERSECRET")

		installLogServer(t, func(*http.Request) *http.Response { return nil })
		workflowLogHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection reset")
		})}
		out, err = callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 59, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "connection reset")
		require.NotContains(t, out, "SUPERSECRET")
	})

	t.Run("insecure or local redirect targets are refused", func(t *testing.T) {
		for _, loc := range []string{"http://logs.example.com/x", "https://169.254.169.254/latest/meta-data", "file:///etc/passwd"} {
			f := newFakeGitHub(t)
			routeJobLogs(f, 60, loc)
			f.install()
			urls := installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, "secret") })
			r := NewRegistry()
			registerGetWorkflowRunLogsTool(r, "", "")
			out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 60, "repo": "o/r"})
			require.NoError(t, err)
			require.Contains(t, out, "logs unavailable: refusing", loc)
			require.Empty(t, *urls, "nothing may be requested for %s", loc)
		}
	})
}

func TestGetWorkflowRunLogsToolRun(t *testing.T) {
	ctx := context.Background()
	const jobsJSON = `{"total_count":3,"jobs":[
 {"id":1,"name":"build","status":"completed","conclusion":"success"},
 {"id":2,"name":"test","status":"completed","conclusion":"failure"},
 {"id":3,"name":"lint","status":"completed","conclusion":"timed_out"}]}`

	t.Run("returns logs of failed jobs only", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/actions/runs/9/jobs", http.StatusOK, jobsJSON)
		routeJobLogs(f, 2, "https://logs.example.com/2")
		routeJobLogs(f, 3, "https://logs.example.com/3")
		routeJobLogs(f, 1, "https://logs.example.com/1")
		f.install()
		urls := installLogServer(t, func(req *http.Request) *http.Response {
			return textResp(http.StatusOK, "log of "+strings.TrimPrefix(req.URL.Path, "/")+"\nlast line\n")
		})
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")

		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 9, "repo": "o/r"})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"https://logs.example.com/2", "https://logs.example.com/3"}, *urls)
		require.Contains(t, out, "### test (job 2), failure")
		require.Contains(t, out, "### lint (job 3), timed_out")
		require.Contains(t, out, "log of 2")
		require.NotContains(t, out, "log of 1")
	})

	t.Run("failed_only=false includes every job", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/actions/runs/9/jobs", http.StatusOK, jobsJSON)
		for i := 1; i <= 3; i++ {
			routeJobLogs(f, i, fmt.Sprintf("https://logs.example.com/%d", i))
		}
		f.install()
		urls := installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, "x\n") })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 9, "repo": "o/r", "failed_only": false})
		require.NoError(t, err)
		require.Len(t, *urls, 3)
	})

	t.Run("no failed jobs lists the jobs instead", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/actions/runs/9/jobs", http.StatusOK,
			`{"total_count":1,"jobs":[{"id":1,"name":"build","status":"completed","conclusion":"success"}]}`)
		f.install()
		urls := installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, "x\n") })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 9, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "No failed jobs in run 9")
		require.Contains(t, out, "build (job_id=1): completed, success")
		require.Empty(t, *urls)
	})

	t.Run("at most five jobs get logs", func(t *testing.T) {
		var jobs []string
		f := newFakeGitHub(t)
		for i := 1; i <= 8; i++ {
			jobs = append(jobs, fmt.Sprintf(`{"id":%d,"name":"job-%d","status":"completed","conclusion":"failure"}`, i, i))
			routeJobLogs(f, i, fmt.Sprintf("https://logs.example.com/%d", i))
		}
		f.json("GET /repos/o/r/actions/runs/9/jobs", http.StatusOK, `{"total_count":8,"jobs":[`+strings.Join(jobs, ",")+`]}`)
		f.install()
		urls := installLogServer(t, func(*http.Request) *http.Response {
			return textResp(http.StatusOK, strings.Repeat("long line of log output\n", 3000))
		})
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 9, "repo": "o/r", "tail_lines": 1000})
		require.NoError(t, err)
		require.Len(t, *urls, maxJobsWithLogs)
		require.Contains(t, out, "3 more job(s) not shown")
		require.Contains(t, out, "job-8 (job_id=8)")
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
	})

	t.Run("one broken job does not hide the others", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/actions/runs/9/jobs", http.StatusOK, jobsJSON)
		routeJobLogs(f, 3, "https://logs.example.com/3")
		f.install() // job 2 has no logs route
		installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, "good log\n") })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		out, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 9, "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "logs unavailable")
		require.Contains(t, out, "good log")
	})

	t.Run("unknown run", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 404, "repo": "o/r"})
		require.ErrorContains(t, err, "list jobs of run 404")
	})
}

func TestGetWorkflowRunLogsToolArguments(t *testing.T) {
	ctx := context.Background()

	t.Run("validation", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.install()
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")

		_, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"repo": "o/r"})
		require.ErrorContains(t, err, "either job_id or run_id")
		_, err = callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 1, "run_id": 2, "repo": "o/r"})
		require.ErrorContains(t, err, "not both")
		_, err = callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": -1, "repo": "o/r"})
		require.ErrorContains(t, err, "greater than 0")
		_, err = r.HandleCall(ctx, llmToolCall("get_workflow_run_logs", `{"job_id": "x"}`))
		require.ErrorContains(t, err, "unmarshaling get_workflow_run_logs arguments")
		_, err = callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 1, "repo": "nope"})
		require.ErrorContains(t, err, "owner/repo")
		require.Empty(t, f.requests)
	})

	t.Run("missing token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 1, "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		routeJobLogs(f, 1, "https://logs.example.com/1")
		f.json("GET /repos/o/r/actions/runs/9/jobs", http.StatusOK, `{"jobs":[{"id":1,"name":"j","conclusion":"failure"}]}`)
		f.install()
		installLogServer(t, func(*http.Request) *http.Response { return textResp(http.StatusOK, "x\n") })
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 1, "repo": "o/r"})
		require.Error(t, err)
		_, err = callTool(cctx, t, r, "get_workflow_run_logs", map[string]any{"run_id": 9, "repo": "o/r"})
		require.Error(t, err)
	})

	t.Run("cancellation reaches the log download", func(t *testing.T) {
		f := newFakeGitHub(t)
		routeJobLogs(f, 1, "https://logs.example.com/1")
		f.install()
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		installLogServer(t, func(*http.Request) *http.Response {
			cancel() // cancelled while the download is in flight
			return textResp(http.StatusOK, "x\n")
		})
		// The fake transport ignores cancellation after the first check, so use a body that fails.
		workflowLogHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			cancel()
			return nil, req.Context().Err()
		})}
		r := NewRegistry()
		registerGetWorkflowRunLogsTool(r, "", "")
		_, err := callTool(cctx, t, r, "get_workflow_run_logs", map[string]any{"job_id": 1, "repo": "o/r"})
		require.ErrorIs(t, err, context.Canceled)
	})
}
