package util

import (
	"fmt"
	"strings"
)

// DefaultLockFiles is a list of common lockfile names used by various package
// managers across different ecosystems.
var DefaultLockFiles = []string{
	"go.sum",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"bun.lockb",
	"deno.lock",
	"Cargo.lock",
	"poetry.lock",
	"uv.lock",
	"Gemfile.lock",
	"composer.lock",
	"pubspec.lock",
	"mix.lock",
	"flake.lock",
	"MODULE.bazel.lock",
}

// IsLockFile reports whether path names one of the ignored lockfiles —
// either at the repository root (path == name) or in any subdirectory
// (path ends in "/name").
func IsLockFile(path string, ignoredLockFiles []string) bool {
	for _, lf := range ignoredLockFiles {
		if lf == "" {
			continue
		}
		if path == lf {
			return true
		}
		if strings.HasSuffix(path, lf) && len(path) > len(lf) && path[len(path)-len(lf)-1] == '/' {
			return true
		}
	}
	return false
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
