package util

import (
	"context"
	"fmt"
	"io"
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

// MaxGitLimitBytes defines the hard ceiling (10 MB) for RunGitWithLimit to prevent
// excessive memory allocation.
const MaxGitLimitBytes = 10 * 1024 * 1024

// RunGitWithLimit invokes `git <args>` in the given working directory, capturing
// combined stdout+stderr up to limit bytes. If the output exceeds limit, it kills
// the command process to stop further execution and returns the read bytes and truncated=true.
// The limit must be non-negative and <= MaxGitLimitBytes (10 MB).
func RunGitWithLimit(ctx context.Context, dir string, limit int64, args ...string) ([]byte, bool, error) {
	if limit < 0 || limit > MaxGitLimitBytes {
		return nil, false, fmt.Errorf("limit must be between 0 and %d bytes (got %d)", MaxGitLimitBytes, limit)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	cmd.Stderr = cmd.Stdout // Redirect stderr to stdout to capture combined output.

	if err := cmd.Start(); err != nil {
		return nil, false, err
	}

	// Read up to limit + 1 bytes.
	lr := io.LimitReader(stdout, limit+1)
	out, err := io.ReadAll(lr)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, false, err
	}

	if int64(len(out)) > limit {
		// Output exceeded limit, kill the process to stop further output generation.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return out[:limit], true, nil
	}

	// If not truncated, wait for normal completion.
	err = cmd.Wait()
	return out, false, err
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
