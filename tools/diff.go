package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

func FetchGitDiff(ctx context.Context, workingDir, base, head string, ignoredLockFiles []string) (string, []string, error) {
	var diffRange string
	if head == "HEAD" {
		// Use single-dot to include uncommitted changes in the working tree/index
		diffRange = base
	} else {
		// Use triple-dot for comparing the tip of head with the common ancestor of base
		diffRange = fmt.Sprintf("%s...%s", base, head)
	}
	cmdArgs := []string{"diff", diffRange}

	cmdArgs = append(cmdArgs, "--", ".")
	cmdArgs = util.AppendGitExcludeArgs(cmdArgs, ignoredLockFiles)

	out, err := util.RunGit(ctx, workingDir, cmdArgs...)
	if err != nil {
		return "", nil, fmt.Errorf("git diff %s failed in %s: %w\nOutput: %s", diffRange, workingDir, err, string(out))
	}

	diffText := string(out)
	if diffText == "" {
		return "No diff found. The repository is perfectly clean.", nil, nil
	}

	// Get file list
	nameOnlyArgs := []string{"diff", "--name-only", diffRange, "--", "."}
	nameOnlyArgs = util.AppendGitExcludeArgs(nameOnlyArgs, ignoredLockFiles)
	nameOnlyOut, err := util.RunGit(ctx, workingDir, nameOnlyArgs...)
	if err != nil {
		return diffText, nil, nil // Fallback if name-only fails
	}
	files := strings.Split(strings.TrimSpace(string(nameOnlyOut)), "\n")
	var filteredFiles []string
	for _, f := range files {
		if f != "" {
			filteredFiles = append(filteredFiles, f)
		}
	}

	return diffText, filteredFiles, nil
}

// ResolveBase turns a branch name (e.g. a PR base branch from metadata) into the
// commit that a PR diff should be taken from: the merge-base of base and head.
//
// A bare branch name is ambiguous: git resolves it to the local branch, which can
// be arbitrarily stale and would make the diff include everything that landed on
// the base since. So the remote-tracking ref (origin/<base>) is preferred, falling
// back to base as given (local branch, SHA, or other revision). Using the
// merge-base rather than the base tip keeps commits that landed on the base after
// the PR branched off out of the diff and commit list.
//
// The base branch is fetched first (best effort) so that origin/<base> reflects the
// remote rather than whenever the clone was last updated.
//
// If base cannot be resolved locally it is returned unchanged, so the later git
// diff/log reports the failure against the name the caller supplied.
func ResolveBase(ctx context.Context, workingDir, base, head string) string {
	if base == "" {
		return base
	}
	if head == "" {
		head = "HEAD"
	}

	fetchBaseBranch(ctx, workingDir, base)

	var resolved string
	for _, candidate := range []string{"refs/remotes/origin/" + base, base} {
		if sha, ok := resolveCommit(ctx, workingDir, candidate); ok {
			resolved = sha
			break
		}
	}
	if resolved == "" {
		return base
	}

	out, err := util.RunGit(ctx, workingDir, "merge-base", resolved, head)
	if err != nil {
		return resolved
	}
	if mergeBase := strings.TrimSpace(string(out)); mergeBase != "" {
		return mergeBase
	}
	return resolved
}

// baseFetchTimeout bounds the best-effort fetch of the base branch.
const baseFetchTimeout = 60 * time.Second

var fullSHARegexp = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// fetchBaseBranch updates refs/remotes/origin/<base> from origin. It is best effort:
// failures (no origin remote, offline, missing credentials, base is not a branch) are
// ignored and the caller falls back to whatever is available locally. It never uses
// --depth, since shallow fetches truncate history shared with other worktrees.
func fetchBaseBranch(ctx context.Context, workingDir, base string) {
	if fullSHARegexp.MatchString(base) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, baseFetchTimeout)
	defer cancel()

	refspec := fmt.Sprintf("+refs/heads/%[1]s:refs/remotes/origin/%[1]s", base)
	cmd := exec.CommandContext(ctx, "git", "fetch", "--quiet", "--no-tags", "origin", refspec)
	cmd.Dir = workingDir
	// Never block on an interactive credential prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	_ = cmd.Run()
}

func resolveCommit(ctx context.Context, workingDir, rev string) (string, bool) {
	out, err := util.RunGit(ctx, workingDir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(string(out))
	return sha, sha != ""
}

// FetchGitCommits returns a bulleted list of commit subjects (first line of
// message) between base and head, excluding merge commits.
func FetchGitCommits(ctx context.Context, workingDir, base, head string) (string, error) {
	commitRange := fmt.Sprintf("%s..%s", base, head)

	out, err := util.RunGit(ctx, workingDir, "log", "--pretty=format:- %s", "--no-merges", commitRange)
	if err != nil {
		// If git log fails (e.g., due to a shallow clone missing history), we
		// return an error to be handled by the caller.
		return "", fmt.Errorf("git log %s failed: %w. Output: %s", commitRange, err, string(out))
	}

	return string(out), nil
}

func registerLocalGetFileDiff(r *Registry, root, base, head string, ignoredLockFiles []string, diffMapProvider func() map[string]string) {
	def := llm.ToolDef{
		Name:        "get_file_diff",
		Description: "Get the git diff for a specific file between base and head branches. Useful when the full diff is omitted due to size or for targeted inspection.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file to get the diff for.",
				},
			},
			"required": []string{"file_path"},
		},
	}

	type args struct {
		FilePath string `json:"file_path"`
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args args) (string, error) {
		if strings.TrimSpace(args.FilePath) == "" {
			return "", errors.New("get_file_diff failed: file_path cannot be empty")
		}

		relPath := args.FilePath
		if root != "" {
			var err error
			relPath, err = util.ValidateAndRel(root, args.FilePath)
			if err != nil {
				return "", fmt.Errorf("get_file_diff failed: %w", err)
			}
		}
		relPath = filepath.ToSlash(relPath)

		if util.IsLockFile(relPath, ignoredLockFiles) {
			return "Lockfile diffs are ignored by default. Use read_file to inspect lockfile contents directly if needed.", nil
		}

		const maxReturnBytes = 40000

		if diffMapProvider != nil {
			if dm := diffMapProvider(); dm != nil {
				if content, ok := dm[relPath]; ok {
					if len(content) <= maxReturnBytes {
						return content, nil
					}
					return content[:maxReturnBytes] + "\n... (truncated to 40k bytes)", nil
				}
			}
		}

		var diffRange string
		if head == "" || head == "HEAD" {
			diffRange = base
		} else {
			diffRange = fmt.Sprintf("%s...%s", base, head)
		}

		cmdArgs := []string{"diff", diffRange, "--", relPath}
		out, truncated, err := util.RunGitWithLimit(ctx, root, maxReturnBytes, cmdArgs...)
		if err != nil {
			return "", fmt.Errorf("get_file_diff failed: %w\nOutput: %s", err, string(out))
		}

		if len(out) == 0 {
			return fmt.Sprintf("No diff found for %s.", relPath), nil
		}

		res := strings.TrimSpace(string(out))
		if truncated {
			res += "\n... (truncated to 40k bytes)"
		}
		return res, nil
	})
}
