package util

import (
	"context"
	"fmt"
	"os/exec"
)

// RunGit invokes `git <args>` in the given working directory (or the current
// directory when dir is empty) and returns combined stdout+stderr. Callers
// wrap the returned error with their own context; RunGit itself does not
// format the error so callers can inspect the exit code via errors.As
// (*exec.ExitError) where relevant.
func RunGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

// AppendGitExcludeArgs appends git pathspec exclude arguments for each entry in
// ignoredLockFiles to the provided args slice and returns the updated slice.
// For example, if "go.sum" is in the list, it appends ":(exclude)*go.sum".
func AppendGitExcludeArgs(args []string, ignoredLockFiles []string) []string {
	for _, lf := range ignoredLockFiles {
		if lf == "" {
			continue
		}
		args = append(args, fmt.Sprintf(":(exclude)*%s", lf))
	}
	return args
}
