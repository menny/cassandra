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
		lf = strings.TrimSpace(lf)
		if lf == "" {
			continue
		}
		// Efficiently check for exact match or suffix starting with a slash
		if path == lf {
			return true
		}
		if strings.HasSuffix(path, lf) && len(path) > len(lf) && path[len(path)-len(lf)-1] == '/' {
			return true
		}
	}
	return false
}

// GitExcludeArgs returns a slice of git pathspec exclude arguments for each
// entry in ignoredLockFiles. For example, if "go.sum" is in the list, it
// returns [":(exclude)*go.sum"].
func GitExcludeArgs(ignoredLockFiles []string) []string {
	args := make([]string, 0, len(ignoredLockFiles))
	for _, lf := range ignoredLockFiles {
		lf = strings.TrimSpace(lf)
		if lf == "" {
			continue
		}
		args = append(args, fmt.Sprintf(":(exclude)*%s", lf))
	}
	return args
}
