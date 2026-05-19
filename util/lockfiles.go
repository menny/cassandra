package util

import (
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
