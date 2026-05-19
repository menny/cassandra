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
					"description": "Optional directory to search in (relative to repository root).",
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
			dir := args.Directory
			if root != "" {
				var err error
				dir, err = util.ValidateAndRel(root, args.Directory)
				if err != nil {
					return "", fmt.Errorf("grep_files failed: %w", err)
				}
			}
			cmdArgs = append(cmdArgs, "--", dir)
		} else {
			cmdArgs = append(cmdArgs, "--", ".")
		}

		cmdArgs = util.AppendGitExcludeArgs(cmdArgs, ignoredLockFiles)

		out, err := util.RunGit(ctx, root, cmdArgs...)
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 {
				return "No matches found.", nil
			}
			return "", fmt.Errorf("grep_files failed: %w\nOutput: %s", err, string(out))
		}

		output := string(out)
		lines := strings.Split(strings.TrimSpace(output), "\n")
		const maxLines = 100
		if len(lines) > maxLines {
			output = strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n... (truncated, %d more matches)", len(lines)-maxLines)
		}

		return strings.TrimSpace(output), nil
	})
}
