package util

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileOptions configures how a file should be read by ReadBoundedFile.
type ReadFileOptions struct {
	// LineStart is the 1-based line number to start reading from.
	LineStart int
	// LineEnd is the 1-based line number to stop reading at (inclusive).
	LineEnd int
	// TailLines is the number of lines to read from the end of the file.
	// If set, LineStart and LineEnd are ignored.
	TailLines int
	// MaxOutputBytes is the maximum number of bytes to return.
	MaxOutputBytes int
	// MaxLineLength is the maximum length of a single line.
	MaxLineLength int
	// MaxTailBufferBytes is the maximum memory to use for the tail buffer.
	MaxTailBufferBytes int
	// FailIfTooLarge, when true, causes ReadBoundedFile to return an error if
	// the file content (when reading whole file) exceeds MaxWholeFileReadBytes.
	FailIfTooLarge bool
	// MaxWholeFileReadBytes is the maximum bytes allowed for a whole file read.
	MaxWholeFileReadBytes int
}

// ReadBoundedFile reads a file from the root according to the provided options.
// It handles line ranges, tailing, and memory bounds to ensure safe reading.
func ReadBoundedFile(ctx context.Context, root, path string, opts ReadFileOptions) (string, error) {
	f, err := OpenInRoot(root, path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if opts.MaxLineLength <= 0 {
		opts.MaxLineLength = 65536
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = 40000
	}
	if opts.MaxWholeFileReadBytes <= 0 {
		opts.MaxWholeFileReadBytes = opts.MaxOutputBytes
	}

	// If no line limits, read up to MaxWholeFileReadBytes.
	if opts.LineStart <= 0 && opts.LineEnd <= 0 && opts.TailLines <= 0 {
		lr := io.LimitReader(f, int64(opts.MaxWholeFileReadBytes)+1)
		b, err := io.ReadAll(lr)
		if err != nil {
			return "", err
		}
		if len(b) > opts.MaxWholeFileReadBytes {
			if opts.FailIfTooLarge {
				return "", fmt.Errorf("file too large for whole-file read. Use line limits")
			}
		}
		return TruncateString(string(b), opts.MaxOutputBytes), nil
	}

	reader := bufio.NewReader(f)

	if opts.TailLines > 0 {
		if opts.MaxTailBufferBytes <= 0 {
			opts.MaxTailBufferBytes = 1024 * 1024
		}
		tb := NewTailBuffer(opts.TailLines, opts.MaxTailBufferBytes)
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}

			line, err := ReadLimitedLine(ctx, reader, opts.MaxLineLength)
			if len(line) == 0 && err != nil {
				if err == io.EOF {
					break
				}
				return "", err
			}
			tb.Add(line)
			if err == io.EOF {
				break
			}
		}

		lines := tb.Lines()
		strLines := make([]string, len(lines))
		for i, l := range lines {
			strLines[i] = string(l)
		}
		return TruncateLines(strLines, opts.MaxOutputBytes), nil
	}

	start := opts.LineStart
	if start <= 0 {
		start = 1
	}
	end := opts.LineEnd

	var resLines []string
	currentLine := 0
	accumulatedBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		line, err := ReadLimitedLine(ctx, reader, opts.MaxLineLength)
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}

		currentLine++
		if currentLine >= start && (end <= 0 || currentLine <= end) {
			resLines = append(resLines, string(line))
			accumulatedBytes += len(line) + 1 // +1 for newline
			// Early exit if we have already exceeded the output limit to prevent OOM.
			// We check against MaxOutputBytes + buffer for suffix.
			if accumulatedBytes > opts.MaxOutputBytes+32 {
				break
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		if end > 0 && currentLine >= end {
			break
		}
	}

	return TruncateLines(resLines, opts.MaxOutputBytes), nil
}

// ReadLimitedLine reads a single line from r, up to limit bytes. It respects
// context cancellation.
func ReadLimitedLine(ctx context.Context, r *bufio.Reader, limit int) ([]byte, error) {
	line, isPrefix, err := r.ReadLine()
	if err != nil && err != io.EOF {
		return nil, err
	}

	res := make([]byte, 0, len(line))
	res = append(res, line...)

	for isPrefix && len(res) < limit {
		if errCtx := ctx.Err(); errCtx != nil {
			return res, errCtx
		}
		line, isPrefix, err = r.ReadLine()
		if err != nil {
			break
		}
		toAppend := limit - len(res)
		if toAppend > len(line) {
			toAppend = len(line)
		}
		res = append(res, line[:toAppend]...)
	}

	if isPrefix {
		for isPrefix {
			if errCtx := ctx.Err(); errCtx != nil {
				return res, errCtx
			}
			_, isPrefix, err = r.ReadLine()
			if err != nil {
				break
			}
		}
	}

	return res, err
}

// TailBuffer maintains a fixed number of lines with a maximum byte size.
type TailBuffer struct {
	lines     [][]byte
	limit     int
	maxBytes  int
	curBytes  int
	count     int
	oldestIdx int
}

// NewTailBuffer creates a new TailBuffer.
func NewTailBuffer(limit, maxBytes int) *TailBuffer {
	return &TailBuffer{
		lines:    make([][]byte, limit),
		limit:    limit,
		maxBytes: maxBytes,
	}
}

// Add appends a line to the buffer, potentially removing the oldest line(s).
func (b *TailBuffer) Add(line []byte) {
	idx := b.count % b.limit
	if b.lines[idx] != nil {
		b.curBytes -= len(b.lines[idx])
		b.oldestIdx = (idx + 1) % b.limit
	}
	b.lines[idx] = line
	b.curBytes += len(line)
	b.count++

	for b.curBytes > b.maxBytes {
		if b.lines[b.oldestIdx] != nil {
			b.curBytes -= len(b.lines[b.oldestIdx])
			b.lines[b.oldestIdx] = nil
		}
		b.oldestIdx = (b.oldestIdx + 1) % b.limit

		if b.curBytes <= 0 {
			b.curBytes = 0
			break
		}
	}
}

// Lines returns the lines currently in the buffer in order.
func (b *TailBuffer) Lines() [][]byte {
	res := make([][]byte, 0, b.limit)
	for i := 0; i < b.limit; i++ {
		idx := (b.oldestIdx + i) % b.limit
		if b.lines[idx] != nil {
			res = append(res, b.lines[idx])
		}
	}
	return res
}

// WriteFileWithDirs creates any missing parent directories before writing the
// file with 0o644 permissions. It uses 0o755 for any created directories.
func WriteFileWithDirs(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write file to %s: %w", path, err)
	}
	return nil
}

// SanitizeFileName replaces any non-alphanumeric character with an underscore,
// ensuring the string is safe for use as a filesystem component.
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

// SanitizeFileNameWithDefault sanitizes s and returns defaultName if the
// result is empty or consists entirely of underscores.
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
