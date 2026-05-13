package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/menny/cassandra/llm"
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

	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args struct {
			FilePath      string `json:"file_path"`
			LineStart     int    `json:"line_start"`
			LineEnd       int    `json:"line_end"`
			TailLines     int    `json:"tail_lines"`
			MaxReadLength int    `json:"max_read_length"`
		}
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}

		const defaultMaxReadLength = 40000
		const absoluteMaxReadLength = 40000
		const absoluteMaxTailLines = 10000

		maxOutput := args.MaxReadLength
		if maxOutput <= 0 {
			maxOutput = defaultMaxReadLength
		}
		if maxOutput > absoluteMaxReadLength {
			maxOutput = absoluteMaxReadLength
		}

		fullPath := args.FilePath
		if root != "" {
			if !filepath.IsAbs(fullPath) {
				fullPath = filepath.Join(root, fullPath)
			}
			// Security: Ensure the path is within the root directory.
			rel, err := filepath.Rel(root, fullPath)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
				return "", fmt.Errorf("read_file failed: path %q is outside the workspace root", args.FilePath)
			}
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			return "", fmt.Errorf("read_file failed: %w", err)
		}

		// If no line-limiting arguments are provided, return the whole file but only if it's small.
		if args.LineStart <= 0 && args.LineEnd <= 0 && args.TailLines <= 0 {
			if info.Size() > int64(absoluteMaxReadLength) {
				return "", fmt.Errorf("read_file failed: file is too large (%d bytes) to read entirely. Use line_start/line_end or tail_lines", info.Size())
			}
			b, err := os.ReadFile(fullPath)
			if err != nil {
				return "", fmt.Errorf("read_file failed: %w", err)
			}
			content := string(b)
			if len(content) > maxOutput {
				return content[:maxOutput] + "\n... (truncated)", nil
			}
			return content, nil
		}

		f, err := os.Open(fullPath)
		if err != nil {
			return "", fmt.Errorf("read_file failed: %w", err)
		}
		defer f.Close()

		var sb strings.Builder
		reader := bufio.NewReader(f)

		if args.TailLines > 0 {
			tailLines := args.TailLines
			if tailLines > absoluteMaxTailLines {
				tailLines = absoluteMaxTailLines
			}
			// Circular buffer for tail lines.
			// To reduce GC pressure, we'll try to be efficient, though ReadBytes still allocates.
			buffer := make([][]byte, tailLines)
			count := 0
			totalBufferBytes := 0
			const maxTailBufferBytes = 1024 * 1024 // 1MB limit for the circular buffer storage

			for {
				line, err := reader.ReadBytes('\n')
				if len(line) == 0 && err != nil {
					if err == io.EOF {
						break
					}
					return "", fmt.Errorf("read_file failed while reading: %w", err)
				}

				// Remove trailing newline for consistent storage.
				line = bytes.TrimRight(line, "\r\n")

				idx := count % tailLines
				// Subtract old line size from total
				if buffer[idx] != nil {
					totalBufferBytes -= len(buffer[idx])
				}

				buffer[idx] = line
				totalBufferBytes += len(line)
				count++

				if totalBufferBytes > maxTailBufferBytes {
					return "", fmt.Errorf("read_file failed: tail collection exceeded 1MB limit. Use a smaller tail_lines or line ranges")
				}

				if err == io.EOF {
					break
				}
			}

			startIdx := 0
			numToPrint := count
			if count > tailLines {
				startIdx = count % tailLines
				numToPrint = tailLines
			}

			for i := 0; i < numToPrint; i++ {
				line := buffer[(startIdx+i)%tailLines]
				if i > 0 {
					if sb.Len()+1+len(line) > maxOutput {
						sb.WriteString("\n... (truncated)")
						break
					}
					sb.WriteString("\n")
				} else if len(line) > maxOutput {
					// Handle case where even the first line is too long
					sb.Write(line[:maxOutput])
					sb.WriteString("\n... (truncated)")
					break
				}
				sb.Write(line)
			}
			return sb.String(), nil
		}

		// Line range.
		start := args.LineStart
		if start <= 0 {
			start = 1
		}
		end := args.LineEnd

		currentLine := 0
		started := false
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) == 0 && err != nil {
				if err == io.EOF {
					break
				}
				return "", fmt.Errorf("read_file failed while reading: %w", err)
			}

			currentLine++
			if currentLine >= start && (end <= 0 || currentLine <= end) {
				line = bytes.TrimRight(line, "\r\n")
				if started {
					if sb.Len()+1+len(line) > maxOutput {
						sb.WriteString("\n... (truncated)")
						break
					}
					sb.WriteString("\n")
				} else {
					started = true
					if len(line) > maxOutput {
						// Handle case where even the first line is too long
						sb.Write(line[:maxOutput])
						sb.WriteString("\n... (truncated)")
						break
					}
				}
				sb.Write(line)
			}

			if err != nil {
				if err == io.EOF {
					break
				}
				return "", fmt.Errorf("read_file failed while reading: %w", err)
			}
			if end > 0 && currentLine >= end {
				break
			}
		}

		return sb.String(), nil
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

	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args struct {
			Directory string `json:"directory"`
			Query     string `json:"query"`
		}
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}

		dir := args.Directory
		if dir == "" {
			dir = "."
		}
		if root != "" {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			// Security: Ensure the path is within the root directory.
			rel, err := filepath.Rel(root, dir)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
				return "", fmt.Errorf("glob_files failed: path %q is outside the workspace root", args.Directory)
			}
		}

		var matches []string
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // Skip errors reading specific files
			}
			if !d.IsDir() {
				// Simple substring match allows catching things like ".go" or "BUILD"
				if strings.Contains(filepath.Base(path), args.Query) || strings.Contains(path, args.Query) {
					relPath := path
					if root != "" {
						if rel, err := filepath.Rel(root, path); err == nil {
							relPath = rel
						}
					}
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

	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args struct {
			Query           string `json:"query"`
			Directory       string `json:"directory"`
			CaseInsensitive bool   `json:"case_insensitive"`
		}
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}

		// git grep options:
		// --line-number: show line numbers
		// --column: show column numbers
		// --extended-regexp: use extended regex
		// --untracked: search untracked files as well
		// -e: treat the next argument as the pattern, even if it starts with a hyphen
		cmdArgs := []string{"grep", "--line-number", "--column", "--extended-regexp", "--untracked"}
		if args.CaseInsensitive {
			cmdArgs = append(cmdArgs, "-i")
		}
		cmdArgs = append(cmdArgs, "-e", args.Query)

		if args.Directory != "" {
			cmdArgs = append(cmdArgs, "--", args.Directory)
		} else {
			cmdArgs = append(cmdArgs, "--", ".")
		}

		// Filter out lock files as they are usually not relevant and can be huge.
		cmdArgs = appendLockFileExcludes(cmdArgs, ignoredLockFiles)

		// Note: git grep already searches the working tree (unstaged changes) by default.
		// We've also added --untracked to include newly created files.

		out, err := runGit(ctx, root, cmdArgs...)
		if err != nil {
			// git grep returns exit code 1 if no matches are found.
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
