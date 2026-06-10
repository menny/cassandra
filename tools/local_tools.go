package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

func registerLocalReadFile(r *Registry, root string) {
	def := llm.ToolDef{
		Name:        "read_file",
		Description: "Read the contents of a local file. For large files, use line_start/line_end or tail_lines to save tokens.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file to read.",
				},
				"line_start": map[string]any{
					"type":        "integer",
					"description": "The 1-based line number to start reading from. Defaults to 1.",
				},
				"line_end": map[string]any{
					"type":        "integer",
					"description": "The 1-based line number to stop reading at (inclusive). If omitted, reads until the end of the file.",
				},
				"tail_lines": map[string]any{
					"type":        "integer",
					"description": "Read this many lines from the end of the file. If specified, line_start and line_end are ignored.",
				},
				"max_read_length": map[string]any{
					"type":        "integer",
					"description": "The maximum number of bytes to read. Defaults to 40,000. Maximum allowed is 40,000.",
				},
			},
			"required": []string{"file_path"},
		},
	}

	type args struct {
		FilePath      string `json:"file_path"`
		LineStart     int    `json:"line_start"`
		LineEnd       int    `json:"line_end"`
		TailLines     int    `json:"tail_lines"`
		MaxReadLength int    `json:"max_read_length"`
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args args) (string, error) {
		const (
			absoluteMaxReadLength = 40000
			absoluteMaxTailLines  = 10000
			maxLineLength         = 65536
			maxTailBufferBytes    = 1024 * 1024
		)

		maxOutput := args.MaxReadLength
		if maxOutput <= 0 || maxOutput > absoluteMaxReadLength {
			maxOutput = absoluteMaxReadLength
		}

		tailLines := args.TailLines
		if tailLines > absoluteMaxTailLines {
			tailLines = absoluteMaxTailLines
		}

		isWholeFileRead := args.LineStart <= 0 && args.LineEnd <= 0 && args.TailLines <= 0

		opts := util.ReadFileOptions{
			LineStart:             args.LineStart,
			LineEnd:               args.LineEnd,
			TailLines:             tailLines,
			MaxOutputBytes:        maxOutput,
			MaxLineLength:         maxLineLength,
			MaxTailBufferBytes:    maxTailBufferBytes,
			FailIfTooLarge:        isWholeFileRead,
			MaxWholeFileReadBytes: absoluteMaxReadLength,
		}

		content, err := util.ReadBoundedFile(ctx, root, args.FilePath, opts)
		if err != nil {
			return "", fmt.Errorf("read_file failed: %w", err)
		}
		return content, nil
	})
}

func registerLocalGlobFiles(r *Registry, root string) {
	def := llm.ToolDef{
		Name:        "glob_files",
		Description: "Search for files within a directory matching an exact substring or simple glob suffix.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"directory": map[string]any{
					"type":        "string",
					"description": "The root directory to search in, defaults to '.' if empty.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "The substring or extension to match against filenames (e.g. '.go' or 'agent').",
				},
			},
			"required": []string{"query"},
		},
	}

	type args struct {
		Directory string `json:"directory"`
		Query     string `json:"query"`
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args args) (string, error) {
		dir := args.Directory
		if dir == "" {
			dir = "."
		}
		if root != "" {
			var err error
			dir, err = util.ValidatePathInRoot(root, dir)
			if err != nil {
				return "", fmt.Errorf("glob_files failed: %w", err)
			}
		}

		resolvedRoot := root
		if root != "" {
			// Resolve root once to handle symlinks (e.g. macOS /var) so that
			// util.SafeRel produces consistent relative paths during the walk.
			if r, err := util.ValidatePathInRoot(root, ""); err == nil {
				resolvedRoot = r
			}
		}

		var matches []string
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				relPath := util.SafeRel(resolvedRoot, path)
				if strings.Contains(filepath.Base(relPath), args.Query) || strings.Contains(relPath, args.Query) {
					matches = append(matches, relPath)
				}
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("glob_files failed: %w", err)
		}

		if len(matches) == 0 {
			return "No matching files found.", nil
		}
		return strings.Join(matches, "\n"), nil
	})
}

func registerLocalGrepFiles(r *Registry, root string, ignoredLockFiles []string) {
	def := llm.ToolDef{
		Name:        "grep_files",
		Description: "Search for a pattern in the repository using git grep. This includes unstaged changes.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The pattern to search for (extended regex).",
				},
				"directory": map[string]any{
					"type":        "string",
					"description": "Optional directory or glob pattern to search in (relative to repository root), e.g., 'dir/**/*.go'.",
				},
				"case_insensitive": map[string]any{
					"type":        "boolean",
					"description": "If true, performs a case-insensitive search.",
				},
			},
			"required": []string{"query"},
		},
	}

	type args struct {
		Query           string `json:"query"`
		Directory       string `json:"directory"`
		CaseInsensitive bool   `json:"case_insensitive"`
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args args) (string, error) {
		cmdArgs := []string{"grep", "--line-number", "--column", "--extended-regexp", "--untracked"}
		if args.CaseInsensitive {
			cmdArgs = append(cmdArgs, "-i")
		}
		cmdArgs = append(cmdArgs, "-e", args.Query)

		if args.Directory != "" {
			// Prevent path traversal sequences (like "..") anywhere in the path.
			for _, part := range strings.FieldsFunc(args.Directory, func(r rune) bool { return r == '/' || r == '\\' }) {
				if part == ".." {
					return "", fmt.Errorf("grep_files failed: path traversal ('..') not allowed in directory path: %q", args.Directory)
				}
			}

			base, pattern := splitGlob(args.Directory)
			var relBase string
			if base != "" {
				if root != "" {
					var err error
					relBase, err = util.ValidateAndRel(root, base)
					if err != nil {
						return "", fmt.Errorf("grep_files failed: %w", err)
					}
				} else {
					relBase = base
				}
			}

			var pathspec string
			if pattern != "" {
				if relBase != "" {
					pathspec = filepath.Join(relBase, pattern)
				} else {
					pathspec = pattern
				}
			} else {
				pathspec = relBase
			}
			pathspec = filepath.ToSlash(pathspec)
			cmdArgs = append(cmdArgs, "--", pathspec)
		} else {
			cmdArgs = append(cmdArgs, "--", ".")
		}

		cmdArgs = util.AppendGitExcludeArgs(cmdArgs, ignoredLockFiles)

		const maxScanBytes = 100000
		const maxReturnBytes = 40000

		out, scannedTruncated, err := util.RunGitWithLimit(ctx, root, maxScanBytes, cmdArgs...)
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 {
				return "No matches found.", nil
			}
			return "", fmt.Errorf("grep_files failed: %w\nOutput: %s", err, string(out))
		}

		if len(out) == 0 {
			return "No matches found.", nil
		}

		output := string(out)
		if len(output) <= maxReturnBytes {
			return strings.TrimSpace(output), nil
		}

		// Truncate output to 40k bytes.
		// Find the last newline in the first maxReturnBytes.
		cutoff := maxReturnBytes
		if lastNL := strings.LastIndex(output[:maxReturnBytes], "\n"); lastNL != -1 {
			cutoff = lastNL + 1
		}

		truncatedPart := output[cutoff:]
		truncatedPart = strings.TrimSpace(truncatedPart)
		var moreMatches int
		if len(truncatedPart) > 0 {
			moreMatches = strings.Count(truncatedPart, "\n") + 1
		}

		result := strings.TrimSpace(output[:cutoff])
		if scannedTruncated {
			result += "\n... (truncated to 40k bytes, there are many more matches. Please refine your query)"
		} else {
			result += fmt.Sprintf("\n... (truncated to 40k bytes, there are %d more matches. Please refine your query)", moreMatches)
		}
		return result, nil
	})
}

// splitGlob splits a pathspec into a literal base directory and a glob pattern.
// It finds the first occurrence of any wildcard character (*, ?, [) and splits the path
// at the last directory separator before that wildcard.
func splitGlob(path string) (string, string) {
	idx := strings.IndexAny(path, "*?[")
	if idx == -1 {
		return path, ""
	}
	// Find the last path separator before the wildcard.
	// We handle both '/' and '\\' separators.
	lastSlash := strings.LastIndex(path[:idx], "/")
	if lastSlashWin := strings.LastIndex(path[:idx], "\\"); lastSlashWin > lastSlash {
		lastSlash = lastSlashWin
	}
	if lastSlash == -1 {
		return "", path
	}
	return path[:lastSlash], path[lastSlash+1:]
}
