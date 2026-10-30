package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

func FetchGitDiff(ctx context.Context, workingDir, base, head string, ignoredLockFiles []string) (string, []string, error) {
	var diffRange string
	if head == "HEAD" {
		// Use single-dot to include uncommitted changes in the working tree/index
		diffRange = base
	} else {
		// Use triple-dot for comparing the tip of head with the common ancestor of base
		diffRange = fmt.Sprintf("%s...%s", base, head)
	}
	cmdArgs := []string{"diff", diffRange}

	cmdArgs = append(cmdArgs, "--", ".")
	cmdArgs = util.AppendGitExcludeArgs(cmdArgs, ignoredLockFiles)

	out, err := util.RunGit(ctx, workingDir, cmdArgs...)
	if err != nil {
		return "", nil, fmt.Errorf("git diff %s failed in %s: %w\nOutput: %s", diffRange, workingDir, err, string(out))
	}

	diffText := string(out)
	if diffText == "" {
		return "No diff found. The repository is perfectly clean.", nil, nil
	}

	// Get file list
	nameOnlyArgs := []string{"diff", "--name-only", diffRange, "--", "."}
	nameOnlyArgs = util.AppendGitExcludeArgs(nameOnlyArgs, ignoredLockFiles)
	nameOnlyOut, err := util.RunGit(ctx, workingDir, nameOnlyArgs...)
	if err != nil {
		return diffText, nil, nil // Fallback if name-only fails
	}
	files := strings.Split(strings.TrimSpace(string(nameOnlyOut)), "\n")
	var filteredFiles []string
	for _, f := range files {
		if f != "" {
			filteredFiles = append(filteredFiles, f)
		}
	}

	return diffText, filteredFiles, nil
}

// FetchGitCommits returns a bulleted list of commit subjects (first line of
// message) between base and head, excluding merge commits.
func FetchGitCommits(ctx context.Context, workingDir, base, head string) (string, error) {
	commitRange := fmt.Sprintf("%s..%s", base, head)

	out, err := util.RunGit(ctx, workingDir, "log", "--pretty=format:- %s", "--no-merges", commitRange)
	if err != nil {
		// If git log fails (e.g., due to a shallow clone missing history), we
		// return an error to be handled by the caller.
		return "", fmt.Errorf("git log %s failed: %w. Output: %s", commitRange, err, string(out))
	}

	return string(out), nil
}

func registerLocalGetFileDiff(r *Registry, root, base, head string, ignoredLockFiles []string, diffMapProvider func() map[string]string) {
	def := llm.ToolDef{
		Name:        "get_file_diff",
		Description: "Get the git diff for a specific file between base and head branches. Useful when the full diff is omitted due to size or for targeted inspection.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Relative path to the file to get the diff for.",
				},
			},
			"required": []string{"file_path"},
		},
	}

	type args struct {
		FilePath string `json:"file_path"`
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args args) (string, error) {
		if strings.TrimSpace(args.FilePath) == "" {
			return "", errors.New("get_file_diff failed: file_path cannot be empty")
		}

		relPath := args.FilePath
		if root != "" {
			var err error
			relPath, err = util.ValidateAndRel(root, args.FilePath)
			if err != nil {
				return "", fmt.Errorf("get_file_diff failed: %w", err)
			}
		}
		relPath = filepath.ToSlash(relPath)

		if util.IsLockFile(relPath, ignoredLockFiles) {
			return "Lockfile diffs are ignored by default. Use read_file to inspect lockfile contents directly if needed.", nil
		}

		const maxReturnBytes = 40000

		if diffMapProvider != nil {
			if dm := diffMapProvider(); dm != nil {
				if content, ok := dm[relPath]; ok {
					if len(content) <= maxReturnBytes {
						return content, nil
					}
					return content[:maxReturnBytes] + "\n... (truncated to 40k bytes)", nil
				}
			}
		}

		var diffRange string
		if head == "" || head == "HEAD" {
			diffRange = base
		} else {
			diffRange = fmt.Sprintf("%s...%s", base, head)
		}

		cmdArgs := []string{"diff", diffRange, "--", relPath}
		out, truncated, err := util.RunGitWithLimit(ctx, root, maxReturnBytes, cmdArgs...)
		if err != nil {
			return "", fmt.Errorf("get_file_diff failed: %w\nOutput: %s", err, string(out))
		}

		if len(out) == 0 {
			return fmt.Sprintf("No diff found for %s.", relPath), nil
		}

		res := strings.TrimSpace(string(out))
		if truncated {
			res += "\n... (truncated to 40k bytes)"
		}
		return res, nil
	})
}
