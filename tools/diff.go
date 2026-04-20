package tools

import (
	"fmt"
	"os/exec"
	"strings"
)

var LockFiles = []string{
	"go.sum",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"Cargo.lock",
	"poetry.lock",
	"Gemfile.lock",
}

// appendLockFileExcludes appends a git pathspec ":(exclude)*<name>" for each
// entry in LockFiles to args, returning the grown slice. Used to suppress
// noisy lockfile churn in diff and grep output.
func appendLockFileExcludes(args []string) []string {
	for _, lf := range LockFiles {
		args = append(args, fmt.Sprintf(":(exclude)*%s", lf))
	}
	return args
}

// IsLockFile reports whether path names a known dependency lockfile —
// either at the repository root (path == name) or in any subdirectory
// (path ends in "/name"). Used by callers that operate on pre-computed
// paths (e.g. PR file lists) rather than via git pathspec excludes.
func IsLockFile(path string) bool {
	for _, lf := range LockFiles {
		if path == lf || strings.HasSuffix(path, "/"+lf) {
			return true
		}
	}
	return false
}

// runGit invokes `git <args>` in the given working directory (or the current
// directory when dir is empty) and returns combined stdout+stderr. Callers
// wrap the returned error with their own context; runGit itself does not
// format the error so callers can inspect the exit code via errors.As
// (*exec.ExitError) where relevant.
func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

func FetchGitDiff(workingDir, base, head string) (string, []string, error) {
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
	cmdArgs = appendLockFileExcludes(cmdArgs)

	out, err := runGit(workingDir, cmdArgs...)
	if err != nil {
		return "", nil, fmt.Errorf("git diff %s failed in %s: %w\nOutput: %s", diffRange, workingDir, err, string(out))
	}

	diffText := string(out)
	if diffText == "" {
		return "No diff found. The repository is perfectly clean.", nil, nil
	}

	// Get file list
	nameOnlyArgs := appendLockFileExcludes([]string{"diff", "--name-only", diffRange, "--", "."})
	nameOnlyOut, err := runGit(workingDir, nameOnlyArgs...)
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

// FetchGitCommits retrieves a list of commit messages between base and head.
func FetchGitCommits(workingDir, base, head string) (string, error) {
	var commitRange string
	if head == "HEAD" {
		commitRange = base + "..HEAD"
	} else {
		commitRange = fmt.Sprintf("%s..%s", base, head)
	}

	// Use a clean format for commit messages
	out, err := runGit(workingDir, "log", "--pretty=format:- %s", "--no-merges", commitRange)
	if err != nil {
		// If git log fails (e.g., shallow clone), we return an error to be handled by the caller.
		return "", fmt.Errorf("git log %s failed: %w. Output: %s", commitRange, err, string(out))
	}

	return string(out), nil
}
