package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

var gitHubClientFactory = func(token string) *github.Client {
	return github.NewClient(nil).WithAuthToken(token)
}

type prCommentArgs struct {
	Comment   string `json:"comment"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	Repo      string `json:"repo,omitempty"`
	PRNumber  int    `json:"pr_number,omitempty"`
}

type prMetadataFile struct {
	RepoFullName string `json:"repo_full_name"`
	PRNumber     int    `json:"pr_number"`
}

func parseRepoSlug(raw string) (owner, repo string, err error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	if strings.Contains(raw, "github.com:") {
		parts := strings.Split(raw, "github.com:")
		raw = parts[1]
	} else if strings.Contains(raw, "github.com/") {
		parts := strings.Split(raw, "github.com/")
		raw = parts[1]
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected format 'owner/repo', got %q", raw)
	}
	return parts[0], parts[1], nil
}

func resolvePRTarget(ctx context.Context, rootDir, metadataFile, repoArg string, prNumArg int) (owner, repo string, prNumber int, err error) {
	if repoArg != "" {
		owner, repo, err = parseRepoSlug(repoArg)
		if err != nil {
			return "", "", 0, err
		}
	}
	if prNumArg > 0 {
		prNumber = prNumArg
	}

	// If either is missing, attempt to read from metadata file
	if (owner == "" || repo == "" || prNumber == 0) && metadataFile != "" {
		if data, readErr := os.ReadFile(metadataFile); readErr == nil {
			var meta prMetadataFile
			if unmarshalErr := json.Unmarshal(data, &meta); unmarshalErr == nil {
				if (owner == "" || repo == "") && meta.RepoFullName != "" {
					if o, r, e := parseRepoSlug(meta.RepoFullName); e == nil {
						owner, repo = o, r
					}
				}
				if prNumber == 0 && meta.PRNumber > 0 {
					prNumber = meta.PRNumber
				}
			}
		}
	}

	// If repo still missing, attempt git remote
	if (owner == "" || repo == "") && rootDir != "" {
		if out, gitErr := util.RunGit(ctx, rootDir, "remote", "get-url", "origin"); gitErr == nil {
			if o, r, e := parseRepoSlug(string(out)); e == nil {
				owner, repo = o, r
			}
		}
	}

	// If PR number still missing, attempt gh pr view
	if prNumber == 0 && rootDir != "" {
		cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "number", "-q", ".number")
		cmd.Dir = rootDir
		if out, ghErr := cmd.Output(); ghErr == nil {
			if n, convErr := strconv.Atoi(strings.TrimSpace(string(out))); convErr == nil && n > 0 {
				prNumber = n
			}
		}
	}

	if owner == "" || repo == "" {
		return "", "", 0, errors.New("could not determine repository; specify 'repo' parameter as 'owner/repo'")
	}
	if prNumber == 0 {
		return "", "", 0, errors.New("could not determine pull request number; specify 'pr_number' parameter")
	}

	return owner, repo, prNumber, nil
}

func registerPRCommentTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "post_pr_comment",
		Description: "Post a comment on a GitHub pull request. Supports either a top-level PR discussion comment " +
			"(when path and line are omitted) or an inline code comment on a specific file line or multi-line range. " +
			"This tool ONLY posts comments; it cannot approve or reject PRs. Only call this tool when explicitly " +
			"instructed by the user.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"comment": map[string]any{
					"type":        "string",
					"description": "Markdown body of the comment.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional repository-relative file path (e.g. 'core/reviewer.go') for an inline comment. Omit for a top-level PR comment.",
				},
				"line": map[string]any{
					"type":        "integer",
					"description": "Optional line number in the new file (diff right side). Required for inline comments.",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "Optional starting line number for multi-line comments. If omitted, comments on a single line.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to auto-detected repository.",
				},
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "Optional pull request number. Defaults to auto-detected PR number.",
				},
			},
			"required": []string{"comment"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args prCommentArgs) (string, error) {
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			token = os.Getenv("GH_TOKEN")
		}
		if token == "" {
			return "", errors.New("GITHUB_TOKEN (or GH_TOKEN) environment variable is required to post PR comments")
		}

		commentBody := strings.TrimSpace(args.Comment)
		if commentBody == "" {
			return "", errors.New("comment body cannot be empty")
		}

		owner, repo, prNumber, err := resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, args.PRNumber)
		if err != nil {
			return "", err
		}

		client := gitHubClientFactory(token)

		// Inline code comment
		if args.Path != "" && args.Line > 0 {
			// Find commit SHA: try to fetch PR head commit
			pr, _, prErr := client.PullRequests.Get(ctx, owner, repo, prNumber)
			var commitSHA string
			if prErr == nil && pr != nil && pr.GetHead() != nil {
				commitSHA = pr.GetHead().GetSHA()
			}
			if commitSHA == "" && rootDir != "" {
				if out, gitErr := util.RunGit(ctx, rootDir, "rev-parse", "HEAD"); gitErr == nil {
					commitSHA = strings.TrimSpace(string(out))
				}
			}
			if commitSHA == "" {
				return "", errors.New("could not resolve head commit SHA for inline comment")
			}

			tag := "<!-- cassandra-inline-comment -->"
			fullBody := fmt.Sprintf("%s\n\n%s", commentBody, tag)

			commentReq := &github.PullRequestComment{
				Body:     github.Ptr(fullBody),
				Path:     github.Ptr(args.Path),
				Line:     github.Ptr(args.Line),
				CommitID: github.Ptr(commitSHA),
				Side:     github.Ptr("RIGHT"),
			}
			if args.StartLine > 0 && args.StartLine != args.Line {
				commentReq.StartLine = github.Ptr(args.StartLine)
				commentReq.StartSide = github.Ptr("RIGHT")
			}

			comment, resp, err := client.PullRequests.CreateComment(ctx, owner, repo, prNumber, commentReq)
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
					return "", fmt.Errorf("GitHub rejected inline comment at %s:%d (likely not part of the PR diff or invalid line): %w", args.Path, args.Line, err)
				}
				return "", fmt.Errorf("failed to create inline PR comment: %w", err)
			}

			result := map[string]any{
				"success":    true,
				"type":       "inline",
				"id":         comment.GetID(),
				"html_url":   comment.GetHTMLURL(),
				"path":       args.Path,
				"line":       args.Line,
				"start_line": args.StartLine,
			}
			resJSON, _ := json.Marshal(result)
			return string(resJSON), nil
		}

		// Top-level PR discussion comment
		tag := "<!-- cassandra-pr-comment -->"
		fullBody := fmt.Sprintf("%s\n\n%s", commentBody, tag)

		commentReq := &github.IssueComment{
			Body: github.Ptr(fullBody),
		}

		comment, _, err := client.Issues.CreateComment(ctx, owner, repo, prNumber, commentReq)
		if err != nil {
			return "", fmt.Errorf("failed to create top-level PR comment: %w", err)
		}

		result := map[string]any{
			"success":  true,
			"type":     "top_level",
			"id":       comment.GetID(),
			"html_url": comment.GetHTMLURL(),
			"repo":     fmt.Sprintf("%s/%s", owner, repo),
			"pr":       prNumber,
		}
		resJSON, _ := json.Marshal(result)
		return string(resJSON), nil
	})
}
