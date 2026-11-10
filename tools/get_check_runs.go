package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

const maxCheckRunPages = 5

type getCheckRunsArgs struct {
	Ref          string `json:"ref,omitempty"`
	PRNumber     int    `json:"pr_number,omitempty"`
	Repo         string `json:"repo,omitempty"`
	OnlyFailures bool   `json:"only_failures,omitempty"`
}

// isFailureConclusion reports whether a check run / job conclusion means "did not succeed".
func isFailureConclusion(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
		return true
	}
	return false
}

// checkRunRank orders failures first, then unfinished runs, then everything else.
func checkRunRank(c *github.CheckRun) int {
	switch {
	case isFailureConclusion(c.GetConclusion()):
		return 0
	case c.GetStatus() != "completed":
		return 1
	}
	return 2
}

func formatCheckRuns(owner, repo, sha, prLabel string, runs []*github.CheckRun, total int, combined *github.CombinedStatus, combinedErr error, onlyFailures bool) string {
	var failed, running int
	for _, c := range runs {
		switch checkRunRank(c) {
		case 0:
			failed++
		case 1:
			running++
		}
	}
	shown := make([]*github.CheckRun, 0, len(runs))
	for _, c := range runs {
		if onlyFailures && checkRunRank(c) != 0 {
			continue
		}
		shown = append(shown, c)
	}
	sort.SliceStable(shown, func(i, j int) bool { return checkRunRank(shown[i]) < checkRunRank(shown[j]) })

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Check runs for %s/%s@%s%s\n", owner, repo, shortSHA(sha), prLabel)
	fmt.Fprintf(&sb, "%d check runs: %d failed, %d not finished, %d passed or neutral", len(runs), failed, running, len(runs)-failed-running)
	if total > len(runs) {
		fmt.Fprintf(&sb, " (GitHub reports %d in total; only the first %d were fetched)", total, len(runs))
	}
	sb.WriteString("\n\n")
	if len(runs) == 0 {
		sb.WriteString("(no check runs)\n")
	}
	for _, c := range shown {
		state := c.GetConclusion()
		if c.GetStatus() != "completed" {
			state = c.GetStatus()
		}
		fmt.Fprintf(&sb, "- [%s] %s (id=%d", state, c.GetName(), c.GetID())
		if app := c.GetApp().GetName(); app != "" {
			fmt.Fprintf(&sb, ", app=%s", app)
		}
		sb.WriteString(")")
		if out := c.GetOutput(); out != nil {
			title, summary := oneLine(out.GetTitle(), 120), oneLine(out.GetSummary(), 240)
			switch {
			case title != "" && summary != "":
				fmt.Fprintf(&sb, " - %s: %s", title, summary)
			case title != "" || summary != "":
				fmt.Fprintf(&sb, " - %s%s", title, summary)
			}
		}
		if url := c.GetHTMLURL(); url != "" {
			fmt.Fprintf(&sb, " <%s>", url)
		}
		sb.WriteString("\n")
	}

	switch {
	case combinedErr != nil:
		fmt.Fprintf(&sb, "\nCommit statuses unavailable: %v\n", combinedErr)
	case combined != nil && len(combined.Statuses) > 0:
		fmt.Fprintf(&sb, "\n#### Commit statuses: %s\n", combined.GetState())
		for _, s := range combined.Statuses {
			if onlyFailures && s.GetState() == "success" {
				continue
			}
			fmt.Fprintf(&sb, "- [%s] %s", s.GetState(), s.GetContext())
			if d := oneLine(s.GetDescription(), 160); d != "" {
				fmt.Fprintf(&sb, " - %s", d)
			}
			if u := s.GetTargetURL(); u != "" {
				fmt.Fprintf(&sb, " <%s>", u)
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nFor GitHub Actions checks, the id is also the job_id accepted by get_workflow_run_logs.\n")
	return sb.String()
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func registerGetCheckRunsTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "get_check_runs",
		Description: "Get the CI status of a commit or pull request: every check run (name, status, conclusion, summary, URL) " +
			"plus legacy commit statuses, with failures listed first. Use it to tell whether CI is red, still running or green, " +
			"and which job failed. Pass 'ref' (branch, tag or SHA) or 'pr_number' (the head commit of the pull request is used).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Branch, tag or commit SHA. Takes precedence over pr_number.",
				},
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "Pull request number; its head commit is inspected. Defaults to the auto-detected pull request when 'ref' is omitted.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
				"only_failures": map[string]any{
					"type":        "boolean",
					"description": "Only list failed check runs and non-successful commit statuses.",
				},
			},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getCheckRunsArgs) (string, error) {
		if args.PRNumber < 0 {
			return "", fmt.Errorf("pr_number must be greater than 0")
		}
		if args.Ref != "" {
			if err := validateGitHubRef("ref", args.Ref); err != nil {
				return "", err
			}
		}
		client, err := githubClientForTool("get check runs")
		if err != nil {
			return "", err
		}

		var (
			owner, repo string
			ref         = args.Ref
			prLabel     string
		)
		if ref != "" {
			owner, repo, err = resolveRepoTarget(ctx, rootDir, metadataFile, args.Repo)
			if err != nil {
				return "", err
			}
		} else {
			var prNumber int
			owner, repo, prNumber, err = resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, args.PRNumber)
			if err != nil {
				return "", err
			}
			pr, resp, err := client.PullRequests.Get(ctx, owner, repo, prNumber)
			if err != nil {
				return "", githubAPIError(fmt.Sprintf("get PR #%d", prNumber), resp, err)
			}
			ref = pr.GetHead().GetSHA()
			if ref == "" {
				return "", fmt.Errorf("PR #%d has no head commit", prNumber)
			}
			prLabel = fmt.Sprintf(" (PR #%d head)", prNumber)
		}

		opts := &github.ListCheckRunsOptions{Filter: github.Ptr("latest"), ListOptions: github.ListOptions{PerPage: 100}}
		var runs []*github.CheckRun
		total := 0
		for page := 0; page < maxCheckRunPages; page++ {
			res, resp, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
			if err != nil {
				return "", githubAPIError(fmt.Sprintf("list check runs for %s", ref), resp, err)
			}
			runs = append(runs, res.CheckRuns...)
			total = res.GetTotal()
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}

		combined, resp, combinedErr := client.Repositories.GetCombinedStatus(ctx, owner, repo, ref, &github.ListOptions{PerPage: 100})
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if combinedErr != nil {
			combinedErr = githubAPIError("get commit statuses", resp, combinedErr)
		}

		return truncateOutput(formatCheckRuns(owner, repo, ref, prLabel, runs, total, combined, combinedErr, args.OnlyFailures)), nil
	})
}
