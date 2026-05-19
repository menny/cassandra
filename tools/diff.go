package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/menny/cassandra/util"
)

// runGit invokes `git <args>` in the given directory. Callers
// wrap the returned error with their own context.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

func FetchGitDiff(ctx context.Context, workingDir, base, head string, ignoredLockFiles []string) (string, []string, error) {
	var diffRange string
	if head == "HEAD" {
		diffRange = base
	} else {
		diffRange = fmt.Sprintf("%s...%s", base, head)
	}
	cmdArgs := []string{"diff", diffRange}

	cmdArgs = append(cmdArgs, "--", ".")
	cmdArgs = util.AppendGitExcludeArgs(cmdArgs, ignoredLockFiles)

	out, err := runGit(ctx, workingDir, cmdArgs...)
	if err != nil {
		return "", nil, fmt.Errorf("git diff %s failed in %s: %w\nOutput: %s", diffRange, workingDir, err, string(out))
	}

	diffText := string(out)
	if diffText == "" {
		return "No diff found. The repository is perfectly clean.", nil, nil
	}

	nameOnlyArgs := []string{"diff", "--name-only", diffRange, "--", "."}
	nameOnlyArgs = util.AppendGitExcludeArgs(nameOnlyArgs, ignoredLockFiles)
	nameOnlyOut, err := runGit(ctx, workingDir, nameOnlyArgs...)
	if err != nil {
		return diffText, nil, nil
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

func FetchGitCommits(ctx context.Context, workingDir, base, head string) (string, error) {
	var commitRange string
	if head == "HEAD" {
		commitRange = base + "..HEAD"
	} else {
		commitRange = fmt.Sprintf("%s..%s", base, head)
	}

	out, err := runGit(ctx, workingDir, "log", "--pretty=format:- %s", "--no-merges", commitRange)
	if err != nil {
		return "", fmt.Errorf("git log %s failed: %w. Output: %s", commitRange, err, string(out))
	}

	return string(out), nil
}
