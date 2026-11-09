package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

const (
	defaultIssueComments = 20
	maxIssueComments     = 50
	maxIssueCommentPages = 5
)

type getIssueArgs struct {
	IssueNumber     int    `json:"issue_number"`
	Repo            string `json:"repo,omitempty"`
	IncludeComments bool   `json:"include_comments,omitempty"`
	MaxComments     int    `json:"max_comments,omitempty"`
}

func formatIssue(owner, repo string, issue *github.Issue) string {
	var sb strings.Builder
	kind := "Issue"
	if issue.IsPullRequest() {
		kind = "Pull request"
	}
	fmt.Fprintf(&sb, "### %s %s/%s#%d: %s\n", kind, owner, repo, issue.GetNumber(), issue.GetTitle())
	state := issue.GetState()
	if reason := issue.GetStateReason(); reason != "" {
		state += " (" + reason + ")"
	}
	fmt.Fprintf(&sb, "- **State**: %s\n", state)
	fmt.Fprintf(&sb, "- **Author**: %s\n", issue.GetUser().GetLogin())
	fmt.Fprintf(&sb, "- **Created**: %s\n", formatGitHubTime(issue.CreatedAt))
	fmt.Fprintf(&sb, "- **Updated**: %s\n", formatGitHubTime(issue.UpdatedAt))
	if issue.ClosedAt != nil {
		fmt.Fprintf(&sb, "- **Closed**: %s\n", formatGitHubTime(issue.ClosedAt))
	}
	if len(issue.Labels) > 0 {
		names := make([]string, 0, len(issue.Labels))
		for _, l := range issue.Labels {
			names = append(names, l.GetName())
		}
		fmt.Fprintf(&sb, "- **Labels**: %s\n", strings.Join(names, ", "))
	}
	if len(issue.Assignees) > 0 {
		names := make([]string, 0, len(issue.Assignees))
		for _, a := range issue.Assignees {
			names = append(names, a.GetLogin())
		}
		fmt.Fprintf(&sb, "- **Assignees**: %s\n", strings.Join(names, ", "))
	}
	if m := issue.GetMilestone(); m != nil {
		fmt.Fprintf(&sb, "- **Milestone**: %s\n", m.GetTitle())
	}
	fmt.Fprintf(&sb, "- **Comments**: %d\n", issue.GetComments())
	fmt.Fprintf(&sb, "- **URL**: %s\n", issue.GetHTMLURL())
	body := strings.TrimSpace(issue.GetBody())
	if body == "" {
		body = "(no description)"
	}
	fmt.Fprintf(&sb, "\n%s\n", body)
	return sb.String()
}

// fetchRecentIssueComments returns the last `limit` comments of an issue and the total number seen.
func fetchRecentIssueComments(ctx context.Context, client *github.Client, owner, repo string, number, limit int) ([]*github.IssueComment, int, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var all []*github.IssueComment
	for page := 0; page < maxIssueCommentPages; page++ {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, 0, githubAPIError("list issue comments", resp, err)
		}
		all = append(all, comments...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	total := len(all)
	if total > limit {
		all = all[total-limit:]
	}
	return all, total, nil
}

func registerGetIssueTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "get_issue",
		Description: "Get a GitHub issue (or pull request, by number): title, state, author, labels, assignees, milestone and description. " +
			"Use it to read the issue a pull request says it fixes (e.g. 'Fixes #123'). " +
			"Optionally includes the most recent comments.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"issue_number": map[string]any{
					"type":        "integer",
					"description": "The issue number.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
				"include_comments": map[string]any{
					"type":        "boolean",
					"description": "Also return the most recent comments of the issue.",
				},
				"max_comments": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("How many recent comments to include. Defaults to %d, maximum %d.", defaultIssueComments, maxIssueComments),
				},
			},
			"required": []string{"issue_number"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getIssueArgs) (string, error) {
		if args.IssueNumber <= 0 {
			return "", errors.New("issue_number is required and must be greater than 0")
		}
		client, err := githubClientForTool("get an issue")
		if err != nil {
			return "", err
		}
		owner, repo, err := resolveRepoTarget(ctx, rootDir, metadataFile, args.Repo)
		if err != nil {
			return "", err
		}

		issue, resp, err := client.Issues.Get(ctx, owner, repo, args.IssueNumber)
		if err != nil {
			return "", githubAPIError(fmt.Sprintf("get issue #%d", args.IssueNumber), resp, err)
		}
		var sb strings.Builder
		sb.WriteString(formatIssue(owner, repo, issue))

		if args.IncludeComments && issue.GetComments() > 0 {
			limit := args.MaxComments
			if limit <= 0 {
				limit = defaultIssueComments
			}
			if limit > maxIssueComments {
				limit = maxIssueComments
			}
			comments, total, err := fetchRecentIssueComments(ctx, client, owner, repo, args.IssueNumber, limit)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&sb, "\n### Comments (showing last %d of %d)\n", len(comments), total)
			for _, c := range comments {
				fmt.Fprintf(&sb, "\n#### %s on %s (id=%d)\n%s\n", c.GetUser().GetLogin(), formatGitHubTime(c.CreatedAt), c.GetID(), strings.TrimSpace(c.GetBody()))
			}
		}
		return truncateOutput(sb.String()), nil
	})
}
