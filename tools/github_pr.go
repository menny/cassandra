package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

// PRStackItem represents a pull request in a stacked PR chain.
type PRStackItem struct {
	PRNumber int    `json:"pr_number"`
	Title    string `json:"title"`
	Branch   string `json:"branch"`
	Base     string `json:"base"`
	Head     string `json:"head"`
}

// PRSummary represents pull request details and changed files for agent tools.
type PRSummary struct {
	PRNumber     int      `json:"pr_number"`
	RepoFullName string   `json:"repo_full_name"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Author       string   `json:"author"`
	Branch       string   `json:"branch"`
	Base         string   `json:"base"`
	Head         string   `json:"head"`
	ChangedFiles []string `json:"changed_files"`
}

type gitHubStackSummary struct {
	ID           int64              `json:"id"`
	Number       int                `json:"number"`
	PullRequests []gitHubStackPRRef `json:"pull_requests"`
}

type gitHubStackPRRef struct {
	Number int `json:"number"`
}

type gitHubStackDetails struct {
	ID           int64                 `json:"id"`
	Number       int                   `json:"number"`
	PullRequests []gitHubStackPRDetail `json:"pull_requests"`
}

type gitHubStackPRDetail struct {
	ID     int64             `json:"id"`
	Number int               `json:"number"`
	Title  string            `json:"title"`
	Head   gitHubStackBranch `json:"head"`
	Base   gitHubStackBranch `json:"base"`
}

type gitHubStackBranch struct {
	Ref string `json:"ref"`
	SHA string `json:"sha,omitempty"`
}

// FetchPRStack fetches the stacked PR chain for the given PR number using GitHub's native Stacks API.
// If the PR is not part of a stack (or if stacks are not enabled/available), it returns (nil, nil).
func FetchPRStack(ctx context.Context, client *github.Client, owner, repo string, prNumber int) ([]PRStackItem, error) {
	if client == nil {
		return nil, errors.New("github client is nil")
	}
	if owner == "" || repo == "" || prNumber <= 0 {
		return nil, errors.New("owner, repo, and valid prNumber are required")
	}

	req, err := client.NewRequest("GET", fmt.Sprintf("repos/%s/%s/stacks?pull_request=%d", owner, repo, prNumber), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build stacks list request: %w", err)
	}

	var summaries []gitHubStackSummary
	resp, err := client.Do(ctx, req, &summaries)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch stacks: %w", err)
	}
	if len(summaries) == 0 {
		return nil, nil
	}

	stackNum := summaries[0].Number
	req, err = client.NewRequest("GET", fmt.Sprintf("repos/%s/%s/stacks/%d", owner, repo, stackNum), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build stack details request: %w", err)
	}

	var details gitHubStackDetails
	resp, err = client.Do(ctx, req, &details)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch stack details: %w", err)
	}

	if len(details.PullRequests) <= 1 {
		return nil, nil
	}

	items := make([]PRStackItem, len(details.PullRequests))
	for i, pr := range details.PullRequests {
		items[i] = PRStackItem{
			PRNumber: pr.Number,
			Title:    pr.Title,
			Branch:   pr.Head.Ref,
			Base:     pr.Base.Ref,
			Head:     pr.Head.SHA,
		}
	}
	return items, nil
}

// FetchPRFiles fetches the list of changed files for a pull request, excluding ignored lock files.
func FetchPRFiles(ctx context.Context, client *github.Client, owner, repo string, prNumber int, ignoredLockFiles []string) ([]string, error) {
	if client == nil {
		return nil, errors.New("github client is nil")
	}
	opts := &github.ListOptions{
		PerPage: 100,
	}
	var allFiles []string

	for {
		files, resp, err := client.PullRequests.ListFiles(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if path := f.GetFilename(); !util.IsLockFile(path, ignoredLockFiles) {
				allFiles = append(allFiles, path)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allFiles, nil
}

// FetchPRDiff fetches the git diff for a PR. If paths are supplied, only diffs for those paths are returned.
// Lockfiles in ignoredLockFiles are omitted.
func FetchPRDiff(ctx context.Context, client *github.Client, owner, repo string, prNumber int, paths []string, ignoredLockFiles []string) (string, error) {
	if client == nil {
		return "", errors.New("github client is nil")
	}
	diff, resp, err := client.PullRequests.GetRaw(ctx, owner, repo, prNumber, github.RawOptions{Type: github.Diff})
	if err != nil {
		var errResp *github.ErrorResponse
		is406 := (errors.As(err, &errResp) && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotAcceptable) ||
			(resp != nil && resp.StatusCode == http.StatusNotAcceptable) ||
			strings.Contains(err.Error(), "406")

		if is406 {
			if len(paths) > 0 {
				return fetchFilePatches(ctx, client, owner, repo, prNumber, paths, ignoredLockFiles)
			}
			return "", nil
		}
		return "", err
	}

	if len(paths) > 0 {
		diffMap := util.SplitUnifiedDiff(diff)
		var matched []string
		for _, p := range paths {
			if util.IsLockFile(p, ignoredLockFiles) {
				continue
			}
			if chunk, ok := diffMap[p]; ok && strings.TrimSpace(chunk) != "" {
				matched = append(matched, chunk)
			}
		}
		return strings.Join(matched, "\n"), nil
	}

	// Filter out lockfiles from raw diff when no paths filter is specified
	lines := strings.Split(diff, "\n")
	var filteredLines []string
	skipping := false

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			skipping = false
			if idx := strings.LastIndex(line, " b/"); idx != -1 {
				skipping = util.IsLockFile(line[idx+3:], ignoredLockFiles)
			}
		}
		if !skipping {
			filteredLines = append(filteredLines, line)
		}
	}
	return strings.Join(filteredLines, "\n"), nil
}

func fetchFilePatches(ctx context.Context, client *github.Client, owner, repo string, prNumber int, paths []string, ignoredLockFiles []string) (string, error) {
	targetPaths := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !util.IsLockFile(p, ignoredLockFiles) {
			targetPaths[p] = true
		}
	}
	opts := &github.ListOptions{PerPage: 100}
	var matchedChunks []string

	for {
		files, resp, err := client.PullRequests.ListFiles(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return "", err
		}
		for _, f := range files {
			fn := f.GetFilename()
			if targetPaths[fn] && f.GetPatch() != "" {
				chunk := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s", fn, fn, fn, fn, f.GetPatch())
				matchedChunks = append(matchedChunks, chunk)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return strings.Join(matchedChunks, "\n"), nil
}

// FetchPRMetadata fetches the PR summary including title, description, author, branch refs, and changed files.
func FetchPRMetadata(ctx context.Context, client *github.Client, owner, repo string, prNumber int, ignoredLockFiles []string) (*PRSummary, error) {
	if client == nil {
		return nil, errors.New("github client is nil")
	}
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR #%d: %w", prNumber, err)
	}
	files, err := FetchPRFiles(ctx, client, owner, repo, prNumber, ignoredLockFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to get files for PR #%d: %w", prNumber, err)
	}

	return &PRSummary{
		PRNumber:     prNumber,
		RepoFullName: fmt.Sprintf("%s/%s", owner, repo),
		Title:        pr.GetTitle(),
		Description:  pr.GetBody(),
		Author:       pr.GetUser().GetLogin(),
		Branch:       pr.GetHead().GetRef(),
		Base:         pr.GetBase().GetRef(),
		Head:         pr.GetHead().GetSHA(),
		ChangedFiles: files,
	}, nil
}

type getPRMetadataArgs struct {
	PRNumber int    `json:"pr_number"`
	Repo     string `json:"repo,omitempty"`
}

func registerGetPRMetadataTool(r *Registry, rootDir, metadataFile string, ignoredLockFiles []string) {
	def := llm.ToolDef{
		Name: "get_pr_metadata",
		Description: "Get metadata for a GitHub pull request in the stack, including its title, " +
			"description, author, branch, base, head, and changed file paths.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "The pull request number to inspect.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to auto-detected repository.",
				},
			},
			"required": []string{"pr_number"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getPRMetadataArgs) (string, error) {
		if args.PRNumber <= 0 {
			return "", errors.New("pr_number is required and must be greater than 0")
		}

		token := getGitHubToken()
		if token == "" {
			return "", errors.New("GITHUB_TOKEN (or GH_TOKEN) environment variable is required to get PR metadata")
		}

		owner, repo, _, err := resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, args.PRNumber)
		if err != nil {
			return "", err
		}

		client := gitHubClientFactory(token)
		summary, err := FetchPRMetadata(ctx, client, owner, repo, args.PRNumber, ignoredLockFiles)
		if err != nil {
			return "", err
		}

		output := formatPRMetadataSummary(summary)
		const maxReturnBytes = 40000
		if len(output) > maxReturnBytes {
			return output[:maxReturnBytes] + "\n... (truncated to 40k bytes)", nil
		}
		return output, nil
	})
}

func formatPRMetadataSummary(s *PRSummary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "### PR #%d: %s\n", s.PRNumber, s.Title)
	if s.RepoFullName != "" {
		fmt.Fprintf(&sb, "- **Repository**: %s\n", s.RepoFullName)
	}
	if s.Author != "" {
		fmt.Fprintf(&sb, "- **Author**: %s\n", s.Author)
	}
	if s.Branch != "" {
		fmt.Fprintf(&sb, "- **Branch**: %s\n", s.Branch)
	}
	if s.Base != "" {
		fmt.Fprintf(&sb, "- **Base**: %s\n", s.Base)
	}
	if s.Head != "" {
		fmt.Fprintf(&sb, "- **Head**: %s\n", s.Head)
	}
	if s.Description != "" {
		fmt.Fprintf(&sb, "- **Description**:\n%s\n", s.Description)
	}
	sb.WriteString("\n### Changed Files:\n")
	if len(s.ChangedFiles) == 0 {
		sb.WriteString("(no changed files)\n")
	} else {
		for _, f := range s.ChangedFiles {
			fmt.Fprintf(&sb, "- %s\n", f)
		}
	}
	return sb.String()
}

type getPRDiffArgs struct {
	PRNumber int      `json:"pr_number"`
	Paths    []string `json:"paths"`
	Repo     string   `json:"repo,omitempty"`
}

func registerGetPRDiffTool(r *Registry, rootDir, metadataFile string, ignoredLockFiles []string) {
	def := llm.ToolDef{
		Name: "get_pr_diff",
		Description: "Get the git diff for specific file paths in a GitHub pull request in the stack. " +
			"The paths parameter is required and must contain at least one repository-relative file path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "The pull request number to fetch the diff from.",
				},
				"paths": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "List of repository-relative file paths to include in the diff.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to auto-detected repository.",
				},
			},
			"required": []string{"pr_number", "paths"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getPRDiffArgs) (string, error) {
		if args.PRNumber <= 0 {
			return "", errors.New("pr_number is required and must be greater than 0")
		}
		if len(args.Paths) == 0 {
			return "", errors.New("paths is required and must contain at least one file path")
		}

		token := getGitHubToken()
		if token == "" {
			return "", errors.New("GITHUB_TOKEN (or GH_TOKEN) environment variable is required to get PR diff")
		}

		owner, repo, _, err := resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, args.PRNumber)
		if err != nil {
			return "", err
		}

		client := gitHubClientFactory(token)
		diff, err := FetchPRDiff(ctx, client, owner, repo, args.PRNumber, args.Paths, ignoredLockFiles)
		if err != nil {
			return "", err
		}

		if strings.TrimSpace(diff) == "" {
			return fmt.Sprintf("No diff found for the specified paths in PR #%d.", args.PRNumber), nil
		}

		const maxReturnBytes = 40000
		if len(diff) > maxReturnBytes {
			return diff[:maxReturnBytes] + "\n... (truncated to 40k bytes)", nil
		}
		return diff, nil
	})
}
