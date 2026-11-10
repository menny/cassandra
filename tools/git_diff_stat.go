package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

type gitDiffStatArgs struct {
	Base string `json:"base"`
	Head string `json:"head,omitempty"`
	Path string `json:"path,omitempty"`
}

func registerGitDiffStat(r *Registry, root string, ignoredLockFiles []string) {
	def := llm.ToolDef{
		Name: "git_diff_stat",
		Description: "Summarize the changes between two revisions of the local repository as a per-file diffstat " +
			"(files changed, insertions, deletions, renames). It compares 'head' against the merge base of 'base' and 'head' " +
			"(like 'git diff base...head'). Much cheaper than a full diff; use it to decide which files deserve a closer look.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"base": map[string]any{
					"type":        "string",
					"description": "Base branch, tag or SHA (e.g. 'main').",
				},
				"head": map[string]any{
					"type":        "string",
					"description": "Head branch, tag or SHA. Defaults to HEAD.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional repository-relative file or directory to restrict the summary to.",
				},
			},
			"required": []string{"base"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args gitDiffStatArgs) (string, error) {
		if err := validateGitRev("base", args.Base); err != nil {
			return "", err
		}
		head := args.Head
		if head == "" {
			head = "HEAD"
		}
		if err := validateGitRev("head", head); err != nil {
			return "", err
		}
		// Ranges are built here, so refuse revisions that already contain range operators.
		if strings.Contains(args.Base, "..") || strings.Contains(head, "..") {
			return "", errors.New("base and head must be single revisions, not ranges")
		}

		cmdArgs := []string{
			"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--stat=200", "--summary", "-M",
			args.Base + "..." + head, "--",
		}
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

		out, err := runGitTool(ctx, root, "git_diff_stat", cmdArgs...)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			return "No differences found.", nil
		}
		return out, nil
	})
}
