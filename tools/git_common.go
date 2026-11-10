package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/menny/cassandra/util"
)

const (
	// maxToolOutputBytes is the hard cap on the bytes a tool returns to the LLM.
	maxToolOutputBytes = 40000
	truncationNotice   = "\n... (truncated)"

	// shallowCloneWarning is surfaced by the history-oriented git tools when the
	// checkout does not contain the full commit history.
	shallowCloneWarning = "WARNING: this repository is a shallow clone, so its history is truncated. " +
		"Results may be incomplete: older commits are missing and git_blame attributes older lines to the " +
		"oldest available (boundary) commit. GitHub-backed tools such as list_pr_commits and get_file_at_ref " +
		"are not affected."
)

// safeGitRevPattern accepts commit-ish expressions (branches, tags, SHAs, HEAD~2, A..B, A...B,
// @{u}, v1^{commit}). It deliberately rejects a leading '-' (option injection), whitespace,
// control characters, and characters with special meaning to git or URLs (':', '?', '#', '*', '\\').
var safeGitRevPattern = regexp.MustCompile(`^[A-Za-z0-9_@][A-Za-z0-9_./@^~{}+=,-]*$`)

// cutOutput returns at most max bytes of s, preferring to cut at a line boundary and never in the
// middle of a UTF-8 sequence.
func cutOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	if i := strings.LastIndexByte(s[:cut], '\n'); i > cut/2 {
		cut = i
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// truncateOutput caps s at maxToolOutputBytes (including the truncation notice).
func truncateOutput(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	return cutOutput(s, maxToolOutputBytes-len(truncationNotice)) + truncationNotice
}

// validateGitRev rejects revision strings that could be interpreted as options or that contain
// characters git tools should never need.
func validateGitRev(field, rev string) error {
	if rev == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(rev) > 200 || !safeGitRevPattern.MatchString(rev) {
		return fmt.Errorf("invalid %s %q: expected a branch, tag, SHA or revision range (e.g. 'main', 'HEAD~3', 'a1b2c3d', 'main..HEAD')", field, rev)
	}
	return nil
}

// validateFreeText rejects values that are passed to git as the value of a --flag=value argument
// but contain line breaks or NUL bytes.
func validateFreeText(field, v string) error {
	if len(v) > 200 || strings.ContainsAny(v, "\x00\r\n") {
		return fmt.Errorf("invalid %s: must be a single line of at most 200 characters", field)
	}
	return nil
}

// relPathInRoot validates that p stays inside root (resolving symlinks) and returns it as a
// slash-separated path relative to root, safe to pass to git after a "--" separator.
func relPathInRoot(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0) {
		return "", errors.New("invalid path: path is required")
	}
	base := root
	if base == "" {
		base = "."
	}
	rel, err := util.ValidateAndRel(base, p)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	// A leading ':' would be parsed by git as pathspec magic (e.g. ":(top)x", ":!x").
	if strings.HasPrefix(rel, ":") {
		rel = "./" + rel
	}
	return rel, nil
}

// relFilePathInRoot validates that p stays inside root (resolving symlinks) and confirms
// that p targets a file (not a directory), checking both the working tree on disk and
// the git object database at ref (or HEAD if ref is empty).
func relFilePathInRoot(ctx context.Context, root, p, ref string) (string, error) {
	rel, err := relPathInRoot(root, p)
	if err != nil {
		return "", err
	}
	cleanRel := strings.TrimPrefix(rel, "./")
	if cleanRel == "" || cleanRel == "." {
		return "", fmt.Errorf("invalid path %q: path is a directory, not a file", p)
	}

	// 1. Check if the path targets a directory on disk.
	targetPath := filepath.Join(root, cleanRel)
	if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
		return "", fmt.Errorf("invalid path %q: path is a directory, not a file", p)
	}

	// 2. Check if the path targets a directory (tree) in git at ref.
	rev := ref
	if rev == "" {
		rev = "HEAD"
	}
	objType, err := util.RunGit(ctx, root, "cat-file", "-t", rev+":"+cleanRel)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err == nil && strings.TrimSpace(string(objType)) == "tree" {
		return "", fmt.Errorf("invalid path %q: path is a directory, not a file", p)
	}

	return rel, nil
}

// isShallowRepo reports whether the repository in root is a shallow clone.
func isShallowRepo(ctx context.Context, root string) bool {
	out, err := util.RunGit(ctx, root, "rev-parse", "--is-shallow-repository")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// runGitTool runs git in root, caps the output at maxToolOutputBytes and includes git's own output
// in the returned error so the model can diagnose failures. When the repository is shallow, the
// error carries a hint, because a missing revision or merge base is a likely consequence.
func runGitTool(ctx context.Context, root, tool string, args ...string) (string, error) {
	out, truncated, err := util.RunGitWithLimit(ctx, root, maxToolOutputBytes, args...)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil && !truncated {
		msg := fmt.Sprintf("%s failed: %v\nOutput: %s", tool, err, strings.TrimSpace(string(out)))
		if isShallowRepo(ctx, root) {
			msg += "\n" + shallowCloneWarning
		}
		return "", errors.New(msg)
	}
	s := string(out)
	if truncated {
		return cutOutput(s, maxToolOutputBytes-len(truncationNotice)) + truncationNotice, nil
	}
	return s, nil
}

// withShallowWarning prefixes out with the shallow clone warning when applicable.
func withShallowWarning(ctx context.Context, root, out string) string {
	if isShallowRepo(ctx, root) {
		return shallowCloneWarning + "\n\n" + out
	}
	return out
}
