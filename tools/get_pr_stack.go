package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

// maxStackWalk bounds how many pull requests are followed in each direction when the stack is
// inferred from branch names (every step costs one API request).
const maxStackWalk = 20

type getPRStackArgs struct {
	PRNumber int    `json:"pr_number,omitempty"`
	Repo     string `json:"repo,omitempty"`
}

func prStackItem(pr *github.PullRequest) PRStackItem {
	return PRStackItem{
		PRNumber: pr.GetNumber(),
		Title:    pr.GetTitle(),
		Branch:   pr.GetHead().GetRef(),
		Base:     pr.GetBase().GetRef(),
		Head:     pr.GetHead().GetSHA(),
	}
}

// sameRepoPRs keeps the pull requests whose head branch lives in owner/repo itself; same-named
// branches in forks must not be mistaken for stack members. Already visited PRs are dropped.
func sameRepoPRs(prs []*github.PullRequest, owner, repo string, visited map[int]bool) []*github.PullRequest {
	full := owner + "/" + repo
	var out []*github.PullRequest
	for _, p := range prs {
		if visited[p.GetNumber()] || !strings.EqualFold(p.GetHead().GetRepo().GetFullName(), full) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetNumber() < out[j].GetNumber() })
	return out
}

func numbersOf(prs []*github.PullRequest) string {
	nums := make([]string, len(prs))
	for i, p := range prs {
		nums[i] = fmt.Sprintf("#%d", p.GetNumber())
	}
	return strings.Join(nums, ", ")
}

// InferPRStack reconstructs a stack of pull requests from their branches: the parent of a PR is the
// open PR whose head branch is this PR's base branch, and its children are the open PRs based on
// this PR's head branch. It is the fallback for when GitHub's Stacks API has nothing to say.
// The result is ordered bottom (closest to the default branch) to top and includes the PR itself;
// notes describe ambiguities and limits that were hit.
func InferPRStack(ctx context.Context, client *github.Client, owner, repo string, prNumber int) (stack []PRStackItem, notes []string, err error) {
	if client == nil {
		return nil, nil, errors.New("github client is nil")
	}
	if owner == "" || repo == "" || prNumber <= 0 {
		return nil, nil, errors.New("owner, repo, and valid prNumber are required")
	}
	pr, resp, err := client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, nil, githubAPIError(fmt.Sprintf("get PR #%d", prNumber), resp, err)
	}
	visited := map[int]bool{prNumber: true}
	self := prStackItem(pr)

	// Walk down: the parent has a head branch equal to our base branch.
	var below []PRStackItem
	base := pr.GetBase().GetRef()
	for step := 0; ; step++ {
		if step == maxStackWalk {
			notes = append(notes, fmt.Sprintf("Stopped looking for parent PRs after %d steps.", maxStackWalk))
			break
		}
		candidates, resp, err := client.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{
			State: "open", Head: owner + ":" + base, ListOptions: github.ListOptions{PerPage: 20},
		})
		if err != nil {
			return nil, nil, githubAPIError(fmt.Sprintf("list pull requests with head branch %q", base), resp, err)
		}
		parents := sameRepoPRs(candidates, owner, repo, visited)
		if len(parents) == 0 {
			break
		}
		if len(parents) > 1 {
			notes = append(notes, fmt.Sprintf("Multiple open PRs have head branch %q (%s); following %s.", base, numbersOf(parents), numbersOf(parents[:1])))
		}
		parent := parents[0]
		visited[parent.GetNumber()] = true
		below = append([]PRStackItem{prStackItem(parent)}, below...)
		base = parent.GetBase().GetRef()
	}

	// Walk up: children are based on our head branch.
	var above []PRStackItem
	head := pr.GetHead().GetRef()
	for step := 0; ; step++ {
		if step == maxStackWalk {
			notes = append(notes, fmt.Sprintf("Stopped looking for child PRs after %d steps.", maxStackWalk))
			break
		}
		candidates, resp, err := client.PullRequests.List(ctx, owner, repo, &github.PullRequestListOptions{
			State: "open", Base: head, ListOptions: github.ListOptions{PerPage: 20},
		})
		if err != nil {
			return nil, nil, githubAPIError(fmt.Sprintf("list pull requests based on branch %q", head), resp, err)
		}
		children := sameRepoPRs(candidates, owner, repo, visited)
		if len(children) == 0 {
			break
		}
		if len(children) > 1 {
			notes = append(notes, fmt.Sprintf("Multiple open PRs are based on branch %q (%s); following %s. The stack forks here.", head, numbersOf(children), numbersOf(children[:1])))
		}
		child := children[0]
		visited[child.GetNumber()] = true
		above = append(above, prStackItem(child))
		head = child.GetHead().GetRef()
	}

	stack = append(append(below, self), above...)
	return stack, notes, nil
}

func formatPRStack(owner, repo string, prNumber int, stack []PRStackItem, source string, notes []string) string {
	var sb strings.Builder
	if len(stack) <= 1 {
		fmt.Fprintf(&sb, "PR %s/%s#%d is not part of a stack.\n", owner, repo, prNumber)
	} else {
		fmt.Fprintf(&sb, "### PR stack for %s/%s#%d (%d PRs, bottom to top; source: %s)\n", owner, repo, prNumber, len(stack), source)
		for _, item := range stack {
			marker := ""
			if item.PRNumber == prNumber {
				marker = " <- (this PR)"
			}
			fmt.Fprintf(&sb, "- #%d: %s (branch: %s, base: %s, head: %s)%s\n", item.PRNumber, item.Title, item.Branch, item.Base, item.Head, marker)
		}
		sb.WriteString("\nUse get_pr_metadata / get_pr_diff / list_pr_commits with a PR number above to inspect the other layers.\n")
	}
	for _, n := range notes {
		fmt.Fprintf(&sb, "Note: %s\n", n)
	}
	return sb.String()
}

func registerGetPRStackTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "get_pr_stack",
		Description: "Get the stack of pull requests a PR belongs to (stacked PRs, bottom to top, with branches, bases and head SHAs), live. " +
			"It asks GitHub's Stacks API first and, if that has nothing, infers the stack from base/head branch names of open PRs. " +
			"Use it when reviewing one layer of a stack to find the layers below and above it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "A pull request number in the stack. Defaults to the auto-detected pull request.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
			},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getPRStackArgs) (string, error) {
		if args.PRNumber < 0 {
			return "", errors.New("pr_number must be greater than 0")
		}
		client, err := githubClientForTool("get the PR stack")
		if err != nil {
			return "", err
		}
		owner, repo, prNumber, err := resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, args.PRNumber)
		if err != nil {
			return "", err
		}

		var notes []string
		stack, apiErr := FetchPRStack(ctx, client, owner, repo, prNumber)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		source := "GitHub Stacks API"
		if apiErr != nil {
			notes = append(notes, fmt.Sprintf("GitHub's Stacks API failed (%v); the stack was inferred from branch names instead.", apiErr))
		}
		if len(stack) <= 1 {
			var inferNotes []string
			stack, inferNotes, err = InferPRStack(ctx, client, owner, repo, prNumber)
			if err != nil {
				return "", err
			}
			notes = append(notes, inferNotes...)
			source = "inferred from base/head branches of open PRs"
		}
		return truncateOutput(formatPRStack(owner, repo, prNumber, stack, source, notes)), nil
	})
}
