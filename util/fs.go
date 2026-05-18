package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteFileWithDirs creates any missing parent directories before writing the
// file with 0o644 permissions.
func WriteFileWithDirs(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write file to %s: %w", path, err)
	}
	return nil
}

// SanitizeFileName replaces any non-alphanumeric character with an underscore.
func SanitizeFileName(s string) string {
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}

// SanitizeFileNameWithDefault sanitizes a filename and returns a default value
// if the result is empty or consists only of underscores.
func SanitizeFileNameWithDefault(s, defaultName string) string {
	res := SanitizeFileName(s)
	if strings.Trim(res, "_") == "" {
		return defaultName
	}
	return res
}

// ValidatePathInRoot ensures that the given path is within the root directory.
// It resolves symlinks to prevent escaping the root, including cases where
// multiple non-existent components follow a symlink.
func ValidatePathInRoot(root, path string) (string, error) {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for root: %w", err)
	}
	// Resolve symlinks for root too, because on some systems (like macOS)
	// /var is a symlink to /private/var, which can cause Rel to fail.
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	fullPath := path
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(root, fullPath)
	}
	fullPath = filepath.Clean(fullPath)

	// 1. Logical boundary check (preliminary)
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		absFullPath = fullPath
	}
	rel, err := filepath.Rel(root, absFullPath)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("path %q is logically outside the root %q", path, root)
	}

	// 2. Physical boundary check (resolve symlinks)
	// We iterate upwards until we find a component that exists, then validate its resolution.
	curr := fullPath
	for {
		resolved, err := filepath.EvalSymlinks(curr)
		if err == nil {
			// Found an existing component. Check if its resolved path is within root.
			rel, err = filepath.Rel(root, resolved)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
				return "", fmt.Errorf("resolved path %q (from %q) is outside the root %q", resolved, curr, root)
			}

			// If we resolved a prefix of the path, we should return the path constructed from the resolved prefix.
			// This ensures that symlinks are resolved in the returned path.
			if curr != fullPath {
				relToCurr, _ := filepath.Rel(curr, fullPath)
				return filepath.Join(resolved, relToCurr), nil
			}
			return resolved, nil
		}

		if !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to resolve symlinks for %q: %w", curr, err)
		}

		// Handle potential broken symlinks. EvalSymlinks fails with IsNotExist
		// if a symlink exists but its target does not.
		if info, lerr := os.Lstat(curr); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path contains a broken symlink %q which cannot be safely validated", curr)
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			// Reached the root of the filesystem without finding anything that exists.
			// This shouldn't happen if 'root' exists, but as a fallback, we've already done the logical check.
			break
		}
		curr = parent
	}

	return fullPath, nil
}

// OpenInRoot validates the path is within the root (if root is not empty) and opens the file.
func OpenInRoot(root, path string) (*os.File, error) {
	fullPath := path
	if root != "" {
		var err error
		fullPath, err = ValidatePathInRoot(root, path)
		if err != nil {
			return nil, err
		}
	}
	return os.Open(fullPath)
}

// SafeRel returns the relative path from base to target. If an error occurs,
// it returns the original target path.
func SafeRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

// ValidateAndRel validates that target is within root and returns its relative path from root.
func ValidateAndRel(root, target string) (string, error) {
	resolvedTarget, err := ValidatePathInRoot(root, target)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := ValidatePathInRoot(root, "")
	if err != nil {
		return "", err
	}
	return filepath.Rel(resolvedRoot, resolvedTarget)
}
