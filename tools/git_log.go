package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/menny/cassandra/llm"
)

const (
	defaultGitLogCount = 20
	maxGitLogCount     = 100
)

type gitLogArgs struct {
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	MaxCount int    `json:"max_count,omitempty"`
	Author   string `json:"author,omitempty"`
	Since    string `json:"since,omitempty"`
	Follow   bool   `json:"follow,omitempty"`
}

func registerGitLog(r *Registry, root string) {
	def := llm.ToolDef{
		Name: "git_log",
		Description: "Show commit history (hash, date, author, subject) of the local repository, newest first. " +
			"Optionally restrict to a file or directory, an author, or a date. " +
			"In a shallow clone the history is truncated; the output starts with a warning when that is the case.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Branch, tag, SHA or revision range (e.g. 'main..HEAD'). Defaults to HEAD.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional repository-relative file or directory; only commits touching it are listed.",
				},
				"max_count": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of commits. Defaults to %d, maximum %d.", defaultGitLogCount, maxGitLogCount),
				},
				"author": map[string]any{
					"type":        "string",
					"description": "Optional author name or email pattern.",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "Optional date filter, e.g. '2024-01-31' or '2 weeks ago'.",
				},
				"follow": map[string]any{
					"type":        "boolean",
					"description": "Follow the file across renames. Requires 'path' to be a single file.",
				},
			},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args gitLogArgs) (string, error) {
		rev := args.Ref
		if rev == "" {
			rev = "HEAD"
		}
		if err := validateGitRev("ref", rev); err != nil {
			return "", err
		}
		count := args.MaxCount
		if count <= 0 {
			count = defaultGitLogCount
		}
		if count > maxGitLogCount {
			count = maxGitLogCount
		}

		cmdArgs := []string{
			"log", "--no-color", "--no-ext-diff", "--date=short",
			"--pretty=format:%h %ad %an: %s",
			fmt.Sprintf("--max-count=%d", count),
		}
		if args.Author != "" {
			if err := validateFreeText("author", args.Author); err != nil {
				return "", err
			}
			cmdArgs = append(cmdArgs, "--author="+args.Author)
		}
		if args.Since != "" {
			if err := validateFreeText("since", args.Since); err != nil {
				return "", err
			}
			cmdArgs = append(cmdArgs, "--since="+args.Since)
		}
		if args.Follow {
			if args.Path == "" {
				return "", errors.New("'follow' requires 'path' to be set to a single file")
			}
			cmdArgs = append(cmdArgs, "--follow")
		}
		cmdArgs = append(cmdArgs, rev)
		if args.Path != "" {
			rel, err := relPathInRoot(root, args.Path)
			if err != nil {
				return "", err
			}
			cmdArgs = append(cmdArgs, "--", rel)
		}

		out, err := runGitTool(ctx, root, "git_log", cmdArgs...)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			out = "No commits found."
		}
		return withShallowWarning(ctx, root, out), nil
	})
}
