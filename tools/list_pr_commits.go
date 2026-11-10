package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

const (
	defaultPRCommits = 100
	// maxPRCommits is also the most the GitHub API returns for a single pull request.
	maxPRCommits = 250
)

type listPRCommitsArgs struct {
	PRNumber   int    `json:"pr_number,omitempty"`
	Repo       string `json:"repo,omitempty"`
	MaxCommits int    `json:"max_commits,omitempty"`
}

func formatPRCommits(owner, repo string, prNumber int, commits []*github.RepositoryCommit, limited bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "### Commits of %s/%s#%d (%d)\n", owner, repo, prNumber, len(commits))
	for _, c := range commits {
		sha := c.GetSHA()
		if len(sha) > 12 {
			sha = sha[:12]
		}
		author := c.GetCommit().GetAuthor().GetName()
		if login := c.GetAuthor().GetLogin(); login != "" {
			author = login
		}
		date := ""
		if d := c.GetCommit().GetAuthor().GetDate(); !d.IsZero() {
			date = d.UTC().Format("2006-01-02")
		}
		subject, _, _ := strings.Cut(strings.TrimSpace(c.GetCommit().GetMessage()), "\n")
		fmt.Fprintf(&sb, "- %s %s %s: %s\n", sha, date, author, subject)
	}
	if limited {
		fmt.Fprintf(&sb, "... (list limited; more commits exist. GitHub returns at most %d commits per pull request)\n", maxPRCommits)
	}
	return sb.String()
}

func registerListPRCommitsTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "list_pr_commits",
		Description: "List the commits of a GitHub pull request in order (SHA, date, author, subject) as GitHub sees them. " +
			"Works even if the commits are not present in the local (possibly shallow) checkout. " +
			"GitHub returns at most 250 commits per pull request.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "The pull request number. Defaults to the auto-detected pull request.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
				"max_commits": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of commits to list. Defaults to %d, maximum %d.", defaultPRCommits, maxPRCommits),
				},
			},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args listPRCommitsArgs) (string, error) {
		if args.PRNumber < 0 {
			return "", fmt.Errorf("pr_number must be greater than 0")
		}
		client, err := githubClientForTool("list PR commits")
		if err != nil {
			return "", err
		}
		owner, repo, prNumber, err := resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, args.PRNumber)
		if err != nil {
			return "", err
		}
		limit := args.MaxCommits
		if limit <= 0 {
			limit = defaultPRCommits
		}
		if limit > maxPRCommits {
			limit = maxPRCommits
		}

		opts := &github.ListOptions{PerPage: 100}
		var commits []*github.RepositoryCommit
		limited := false
		for {
			page, resp, err := client.PullRequests.ListCommits(ctx, owner, repo, prNumber, opts)
			if err != nil {
				return "", githubAPIError(fmt.Sprintf("list commits of PR #%d", prNumber), resp, err)
			}
			commits = append(commits, page...)
			if len(commits) >= limit {
				limited = len(commits) > limit || resp.NextPage != 0
				commits = commits[:limit]
				break
			}
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
		return truncateOutput(formatPRCommits(owner, repo, prNumber, commits, limited)), nil
	})
}
