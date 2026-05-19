package tools

import (
	"context"
	"fmt"
	"strings"

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
