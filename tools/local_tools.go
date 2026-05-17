package tools

import (
	"bufio"
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

		fullPath := args.FilePath
		if root != "" {
			var err error
			fullPath, err = util.ValidatePathInRoot(root, args.FilePath)
			if err != nil {
				return "", fmt.Errorf("read_file failed: %w", err)
			}
		}

		f, err := os.Open(fullPath)
		if err != nil {
			return "", fmt.Errorf("read_file failed: %w", err)
		}
		defer f.Close()

		// If no line limits, read up to absoluteMaxReadLength.
		// Use LimitReader to protect against infinite pseudo-files (e.g. /dev/zero).
		if args.LineStart <= 0 && args.LineEnd <= 0 && args.TailLines <= 0 {
			lr := io.LimitReader(f, int64(absoluteMaxReadLength)+1)
			b, err := io.ReadAll(lr)
			if err != nil {
				return "", fmt.Errorf("read_file failed: %w", err)
			}
			if len(b) > absoluteMaxReadLength {
				return "", fmt.Errorf("read_file failed: file too large for whole-file read. Use line limits")
			}
			content := string(b)
			if len(content) > maxOutput {
				return content[:maxOutput] + "\n... (truncated)", nil
			}
			return content, nil
		}

		var sb strings.Builder
		reader := bufio.NewReader(f)

		if args.TailLines > 0 {
			limit := args.TailLines
			if limit > absoluteMaxTailLines {
				limit = absoluteMaxTailLines
			}

			tb := newTailBuffer(limit, maxTailBufferBytes)
			for {
				if err := ctx.Err(); err != nil {
					return "", err
				}

				line, err := readLimitedLine(ctx, reader, maxLineLength)
				if len(line) == 0 && err != nil {
					if err == io.EOF {
						break
					}
					return "", fmt.Errorf("read_file failed while reading: %w", err)
				}
				tb.Add(line)
				if err == io.EOF {
					break
				}
			}

			for i, line := range tb.Lines() {
				if stop := writeLine(&sb, line, i > 0, maxOutput); stop {
					break
				}
			}
			return sb.String(), nil
		}

		start := args.LineStart
		if start <= 0 {
			start = 1
		}
		end := args.LineEnd

		currentLine := 0
		started := false
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}

			line, err := readLimitedLine(ctx, reader, maxLineLength)
			if len(line) == 0 && err != nil {
				if err == io.EOF {
					break
				}
				return "", fmt.Errorf("read_file failed while reading: %w", err)
			}

			currentLine++
			if currentLine >= start && (end <= 0 || currentLine <= end) {
				if stop := writeLine(&sb, line, started, maxOutput); stop {
					break
				}
				started = true
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

// writeLine appends a line to sb, adding a newline if started is true.
// Returns true if the output was truncated and no more lines should be written.
func writeLine(sb *strings.Builder, line []byte, started bool, maxOutput int) bool {
	prefix := ""
	if started {
		prefix = "\n"
	}

	if sb.Len()+len(prefix)+len(line) > maxOutput {
		remain := maxOutput - sb.Len() - len(prefix)
		if remain > 0 {
			sb.WriteString(prefix)
			sb.Write(line[:remain])
		}
		sb.WriteString("\n... (truncated)")
		return true
	}

	sb.WriteString(prefix)
	sb.Write(line)
	return false
}

type tailBuffer struct {
	lines     [][]byte
	limit     int
	maxBytes  int
	curBytes  int
	count     int
	oldestIdx int
}

func newTailBuffer(limit, maxBytes int) *tailBuffer {
	return &tailBuffer{
		lines:    make([][]byte, limit),
		limit:    limit,
		maxBytes: maxBytes,
	}
}

func (b *tailBuffer) Add(line []byte) {
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

func (b *tailBuffer) Lines() [][]byte {
	res := make([][]byte, 0, b.limit)
	for i := 0; i < b.limit; i++ {
		idx := (b.oldestIdx + i) % b.limit
		if b.lines[idx] != nil {
			res = append(res, b.lines[idx])
		}
	}
	return res
}

func readLimitedLine(ctx context.Context, r *bufio.Reader, limit int) ([]byte, error) {
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
			var err error
			dir, err = util.ValidatePathInRoot(root, dir)
			if err != nil {
				return "", fmt.Errorf("glob_files failed: %w", err)
			}
		}

		var matches []string
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				relPath := path
				if root != "" {
					if rel, err := filepath.Rel(root, path); err == nil {
						relPath = rel
					}
				}
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

	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args struct {
			Query           string `json:"query"`
			Directory       string `json:"directory"`
			CaseInsensitive bool   `json:"case_insensitive"`
		}
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}

		cmdArgs := []string{"grep", "--line-number", "--column", "--extended-regexp", "--untracked"}
		if args.CaseInsensitive {
			cmdArgs = append(cmdArgs, "-i")
		}
		cmdArgs = append(cmdArgs, "-e", args.Query)

		if args.Directory != "" {
			dir := args.Directory
			if root != "" {
				var err error
				dir, err = util.ValidatePathInRoot(root, args.Directory)
				if err != nil {
					return "", fmt.Errorf("grep_files failed: %w", err)
				}
				// Convert back to relative path for Git consistency and relative output.
				// We only use the relative path if it's clean and doesn't escape.
				if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
					dir = rel
				} else if !filepath.IsAbs(args.Directory) {
					// Fallback to original input if it was relative
					dir = args.Directory
				}
			}
			cmdArgs = append(cmdArgs, "--", dir)
		} else {
			cmdArgs = append(cmdArgs, "--", ".")
		}

		cmdArgs = appendLockFileExcludes(cmdArgs, ignoredLockFiles)

		out, err := runGit(ctx, root, cmdArgs...)
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
