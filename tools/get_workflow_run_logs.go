package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

const (
	defaultLogTailLines = 150
	maxLogTailLines     = 1000
	maxLogLineBytes     = 4096
	maxJobsWithLogs     = 5
)

// maxLogDownloadBytes bounds how much of a job log is streamed; only the tail is kept in memory.
// It is a variable so tests can exercise the cap without generating tens of megabytes.
var maxLogDownloadBytes int64 = 64 * 1024 * 1024

// workflowLogHTTPClient downloads the (pre-signed, unauthenticated) job log URLs returned by the
// GitHub API. It is a variable so tests can substitute a fake transport.
var workflowLogHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects while downloading logs")
		}
		return validateLogURL(req.URL)
	},
}

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

type getWorkflowRunLogsArgs struct {
	JobID      int64  `json:"job_id,omitempty"`
	RunID      int64  `json:"run_id,omitempty"`
	Repo       string `json:"repo,omitempty"`
	TailLines  int    `json:"tail_lines,omitempty"`
	FailedOnly *bool  `json:"failed_only,omitempty"`
}

// validateLogURL guards the log download. The URL comes from GitHub's API response rather than from
// the model, but it is still required to be https and to point at a named host.
func validateLogURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("refusing to download logs from a non-https URL")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("refusing to download logs from a local or IP-literal host")
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// tailLines streams r line by line and keeps only the last n lines. Lines are stripped of ANSI
// escapes and carriage returns and cut at maxLogLineBytes. It returns the kept lines and the total
// number of lines seen.
func tailLines(r io.Reader, n int) ([]string, int, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var (
		kept     []string
		total    int
		cur      []byte
		overflow bool
	)
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, 0, err
		}
		if room := maxLogLineBytes - len(cur); len(chunk) <= room {
			cur = append(cur, chunk...)
		} else {
			if room > 0 {
				cur = append(cur, chunk[:room]...)
			}
			overflow = true
		}
		if isPrefix {
			continue
		}
		line := strings.TrimPrefix(string(cur), "\ufeff")
		line = strings.ReplaceAll(ansiEscapePattern.ReplaceAllString(line, ""), "\r", "")
		if overflow {
			line += " ...(line cut)"
		}
		cur, overflow = cur[:0], false

		total++
		kept = append(kept, line)
		if len(kept) >= 2*n {
			kept = append(kept[:0], kept[len(kept)-n:]...)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return kept, total, nil
}

// downloadLogTail fetches a log from a pre-signed URL and returns its last n lines.
func downloadLogTail(ctx context.Context, logURL *url.URL, n int) (lines []string, total int, cappedInput bool, err error) {
	if err := validateLogURL(logURL); err != nil {
		return nil, 0, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
	if err != nil {
		return nil, 0, false, err
	}
	resp, err := workflowLogHTTPClient.Do(req)
	if err != nil {
		// The URL embeds a signature, so never echo it; url.Error would include it.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, 0, false, fmt.Errorf("downloading logs failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, false, fmt.Errorf("downloading logs failed: unexpected status %s", resp.Status)
	}

	cr := &countingReader{r: io.LimitReader(resp.Body, maxLogDownloadBytes+1)}
	lines, total, err = tailLines(cr, n)
	if err != nil {
		return nil, 0, false, fmt.Errorf("reading logs failed: %w", err)
	}
	return lines, total, cr.n > maxLogDownloadBytes, nil
}

// fitTail keeps the newest lines whose joined size stays within budget bytes.
func fitTail(lines []string, budget int) []string {
	used := 0
	start := len(lines)
	for start > 0 && used+len(lines[start-1])+1 <= budget {
		start--
		used += len(lines[start]) + 1
	}
	return lines[start:]
}

// fetchJobLogSection returns a formatted section with the tail of a job's log.
func fetchJobLogSection(ctx context.Context, client *github.Client, owner, repo string, jobID int64, name, conclusion string, tail, budget int) (string, error) {
	var sb strings.Builder
	title := fmt.Sprintf("job %d", jobID)
	if name != "" {
		title = fmt.Sprintf("%s (job %d)", name, jobID)
	}
	if conclusion != "" {
		title += ", " + conclusion
	}
	fmt.Fprintf(&sb, "### %s\n", title)

	logURL, resp, err := client.Actions.GetWorkflowJobLogs(ctx, owner, repo, jobID, 4)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		fmt.Fprintf(&sb, "(logs unavailable: %v)\n", githubAPIError("get job logs", resp, err))
		return sb.String(), nil
	}
	lines, total, capped, err := downloadLogTail(ctx, logURL, tail)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		fmt.Fprintf(&sb, "(logs unavailable: %v)\n", err)
		return sb.String(), nil
	}

	header := fmt.Sprintf("%d lines in total", total)
	if capped {
		header = fmt.Sprintf("log exceeds %d MB; only its beginning was read, %d lines seen", maxLogDownloadBytes/(1024*1024), total)
	}
	fitted := fitTail(lines, budget-sb.Len()-200)
	fmt.Fprintf(&sb, "(showing the last %d of %s)\n", len(fitted), header)
	sb.WriteString(strings.Join(fitted, "\n"))
	sb.WriteString("\n")
	return sb.String(), nil
}

func registerGetWorkflowRunLogsTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "get_workflow_run_logs",
		Description: "Get the tail of GitHub Actions job logs to find out why CI failed. Pass 'job_id' (for Actions checks this is the " +
			"check run id from get_check_runs) for one job, or 'run_id' for the failed jobs of a workflow run (at most 5 jobs). " +
			"Only the last 'tail_lines' lines of each log are returned, because failures are usually at the end. " +
			"Requires a token with Actions read permission; logs expire after the repository's retention period.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]any{
					"type":        "integer",
					"description": "Workflow job ID (equals the check run id of a GitHub Actions check).",
				},
				"run_id": map[string]any{
					"type":        "integer",
					"description": "Workflow run ID. Logs of its failed jobs are returned (see failed_only).",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
				"tail_lines": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("How many lines from the end of each log to return. Defaults to %d, maximum %d.", defaultLogTailLines, maxLogTailLines),
				},
				"failed_only": map[string]any{
					"type":        "boolean",
					"description": "With run_id: only include failed jobs. Defaults to true; set false to include every job.",
				},
			},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getWorkflowRunLogsArgs) (string, error) {
		if args.JobID < 0 || args.RunID < 0 {
			return "", errors.New("job_id and run_id must be greater than 0")
		}
		if args.JobID == 0 && args.RunID == 0 {
			return "", errors.New("either job_id or run_id is required")
		}
		if args.JobID != 0 && args.RunID != 0 {
			return "", errors.New("specify either job_id or run_id, not both")
		}
		tail := args.TailLines
		if tail <= 0 {
			tail = defaultLogTailLines
		}
		if tail > maxLogTailLines {
			tail = maxLogTailLines
		}

		client, err := githubClientForTool("get workflow logs")
		if err != nil {
			return "", err
		}
		owner, repo, err := resolveRepoTarget(ctx, rootDir, metadataFile, args.Repo)
		if err != nil {
			return "", err
		}

		if args.JobID != 0 {
			var name, conclusion string
			if job, _, jobErr := client.Actions.GetWorkflowJobByID(ctx, owner, repo, args.JobID); jobErr == nil {
				name, conclusion = job.GetName(), job.GetConclusion()
			} else if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			section, err := fetchJobLogSection(ctx, client, owner, repo, args.JobID, name, conclusion, tail, maxToolOutputBytes-200)
			if err != nil {
				return "", err
			}
			return truncateOutput(section), nil
		}

		jobs, resp, err := client.Actions.ListWorkflowJobs(ctx, owner, repo, args.RunID,
			&github.ListWorkflowJobsOptions{Filter: "latest", ListOptions: github.ListOptions{PerPage: 100}})
		if err != nil {
			return "", githubAPIError(fmt.Sprintf("list jobs of run %d", args.RunID), resp, err)
		}
		failedOnly := args.FailedOnly == nil || *args.FailedOnly
		var selected, skipped []*github.WorkflowJob
		for _, j := range jobs.Jobs {
			if failedOnly && !isFailureConclusion(j.GetConclusion()) {
				continue
			}
			if len(selected) < maxJobsWithLogs {
				selected = append(selected, j)
			} else {
				skipped = append(skipped, j)
			}
		}

		var sb strings.Builder
		if len(selected) == 0 {
			fmt.Fprintf(&sb, "No failed jobs in run %d (%d jobs). Jobs:\n", args.RunID, len(jobs.Jobs))
			for _, j := range jobs.Jobs {
				fmt.Fprintf(&sb, "- %s (job_id=%d): %s%s\n", j.GetName(), j.GetID(), j.GetStatus(), conclusionSuffix(j.GetConclusion()))
			}
			sb.WriteString("Pass failed_only=false or a job_id to read a log anyway.\n")
			return truncateOutput(sb.String()), nil
		}
		perJob := (maxToolOutputBytes - 600) / len(selected)
		for _, j := range selected {
			section, err := fetchJobLogSection(ctx, client, owner, repo, j.GetID(), j.GetName(), j.GetConclusion(), tail, perJob)
			if err != nil {
				return "", err
			}
			sb.WriteString(section)
			sb.WriteString("\n")
		}
		if len(skipped) > 0 {
			fmt.Fprintf(&sb, "%d more job(s) not shown; request them with job_id:\n", len(skipped))
			for _, j := range skipped {
				fmt.Fprintf(&sb, "- %s (job_id=%d)%s\n", j.GetName(), j.GetID(), conclusionSuffix(j.GetConclusion()))
			}
		}
		return truncateOutput(sb.String()), nil
	})
}

func conclusionSuffix(c string) string {
	if c == "" {
		return ""
	}
	return ", " + c
}
