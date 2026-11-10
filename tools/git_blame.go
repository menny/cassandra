package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/menny/cassandra/llm"
)

const maxGitBlameLines = 400

type gitBlameArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line,omitempty"`
	Ref       string `json:"ref,omitempty"`
}

func registerGitBlame(r *Registry, root string) {
	def := llm.ToolDef{
		Name: "git_blame",
		Description: "Show which commit and author last changed each line in a line range of a local file. " +
			"Use it to find out why a line exists or who to attribute it to. " +
			"In a shallow clone, older lines are attributed to the oldest available commit; the output starts with a warning when that is the case.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Repository-relative path of the file.",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "1-based first line of the range.",
				},
				"end_line": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("1-based last line of the range (inclusive). Defaults to start_line. At most %d lines per call.", maxGitBlameLines),
				},
				"ref": map[string]any{
					"type":        "string",
					"description": "Optional revision to blame at. Defaults to the working tree.",
				},
			},
			"required": []string{"path", "start_line"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args gitBlameArgs) (string, error) {
		if args.StartLine < 1 {
			return "", errors.New("start_line is required and must be at least 1")
		}
		end := args.EndLine
		if end == 0 {
			end = args.StartLine
		}
		if end < args.StartLine {
			return "", errors.New("end_line must not be smaller than start_line")
		}
		if end-args.StartLine+1 > maxGitBlameLines {
			return "", fmt.Errorf("line range too large: at most %d lines per call", maxGitBlameLines)
		}
		if args.Ref != "" {
			if err := validateGitRev("ref", args.Ref); err != nil {
				return "", err
			}
		}
		rel, err := relFilePathInRoot(ctx, root, args.Path, args.Ref)
		if err != nil {
			return "", err
		}

		cmdArgs := []string{"blame", "--date=short", "-w", "-L", fmt.Sprintf("%d,%d", args.StartLine, end)}
		if args.Ref != "" {
			cmdArgs = append(cmdArgs, args.Ref)
		}
		cmdArgs = append(cmdArgs, "--", rel)

		out, err := runGitTool(ctx, root, "git_blame", cmdArgs...)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			out = "No blame output."
		}
		return withShallowWarning(ctx, root, out), nil
	})
}
