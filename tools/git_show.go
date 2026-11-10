package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

// maxGitShowContentBytes bounds how much of a file is read before line windowing and the final cap.
const maxGitShowContentBytes = 1024 * 1024

type gitShowArgs struct {
	Ref       string `json:"ref"`
	Path      string `json:"path,omitempty"`
	Contents  bool   `json:"contents,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

func registerGitShow(r *Registry, root string, ignoredLockFiles []string) {
	def := llm.ToolDef{
		Name: "git_show",
		Description: "Show a commit (message, diffstat and patch), optionally limited to one path, or - with 'contents' set - " +
			"the contents of a file as it existed at a given revision (e.g. the base branch). " +
			"Works on the local repository only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Commit, branch, tag or SHA to show.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Repository-relative path. Limits the patch to this path, or selects the file when 'contents' is true.",
				},
				"contents": map[string]any{
					"type":        "boolean",
					"description": "When true, print the file 'path' as it was at 'ref' instead of the commit patch.",
				},
				"line_start": map[string]any{
					"type":        "integer",
					"description": "With 'contents': 1-based first line to return.",
				},
				"line_end": map[string]any{
					"type":        "integer",
					"description": "With 'contents': 1-based last line to return (inclusive).",
				},
			},
			"required": []string{"ref"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args gitShowArgs) (string, error) {
		if err := validateGitRev("ref", args.Ref); err != nil {
			return "", err
		}
		if args.Contents {
			return showFileAtRef(ctx, root, args)
		}

		cmdArgs := []string{"show", "--no-color", "--no-ext-diff", "--no-textconv", "--pretty=fuller", "--stat", "--patch", args.Ref, "--"}
		if args.Path != "" {
			rel, err := relPathInRoot(root, args.Path)
			if err != nil {
				return "", err
			}
			cmdArgs = append(cmdArgs, rel)
		} else {
			cmdArgs = append(cmdArgs, ".")
			cmdArgs = util.AppendGitExcludeArgs(cmdArgs, ignoredLockFiles)
		}
		out, err := runGitTool(ctx, root, "git_show", cmdArgs...)
		if err != nil {
			return "", err
		}
		return out, nil
	})
}

func showFileAtRef(ctx context.Context, root string, args gitShowArgs) (string, error) {
	if args.Path == "" {
		return "", errors.New("'path' is required when 'contents' is true")
	}
	rel, err := relFilePathInRoot(ctx, root, args.Path, args.Ref)
	if err != nil {
		return "", err
	}
	rel = strings.TrimPrefix(rel, "./")

	out, truncated, err := util.RunGitWithLimit(ctx, root, maxGitShowContentBytes, "show", "--no-textconv", args.Ref+":"+rel)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil && !truncated {
		msg := fmt.Sprintf("git_show failed: %v\nOutput: %s", err, strings.TrimSpace(string(out)))
		if isShallowRepo(ctx, root) {
			msg += "\n" + shallowCloneWarning
		}
		return "", errors.New(msg)
	}
	if bytes.IndexByte(out, 0) >= 0 {
		return fmt.Sprintf("%s:%s is a binary file; contents not shown.", args.Ref, rel), nil
	}

	text := string(out)
	if args.LineStart > 0 || args.LineEnd > 0 {
		text = windowLines(text, args.LineStart, args.LineEnd)
	}
	result := truncateOutput(text)
	if truncated && !strings.HasSuffix(result, truncationNotice) {
		result += truncationNotice
	}
	return result, nil
}

// windowLines returns lines [start, end] (1-based, inclusive) of text. A start or end of 0 means
// "from the beginning" / "to the end".
func windowLines(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}
