package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

const maxDirectoryEntries = 500

type getFileAtRefArgs struct {
	Path      string `json:"path"`
	Ref       string `json:"ref,omitempty"`
	Repo      string `json:"repo,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

// cleanRepoPath validates a repository-relative path for the GitHub contents API. "." and "/" denote
// the repository root and yield "".
func cleanRepoPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsAny(p, "\\\x00") {
		return "", fmt.Errorf("invalid path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("invalid path %q: '..' is not allowed", p)
		}
	}
	cleaned := path.Clean("/" + p)
	cleaned = strings.TrimPrefix(cleaned, "/")
	return cleaned, nil
}

func formatDirectoryListing(owner, repo, dir, ref string, entries []*github.RepositoryContent) string {
	var sb strings.Builder
	shown := entries
	if len(shown) > maxDirectoryEntries {
		shown = shown[:maxDirectoryEntries]
	}
	display := dir
	if display == "" {
		display = "/"
	}
	fmt.Fprintf(&sb, "# %s/%s:%s @ %s (directory, %d entries)\n", owner, repo, display, ref, len(entries))
	for _, e := range shown {
		switch e.GetType() {
		case "dir":
			fmt.Fprintf(&sb, "%s/\n", e.GetName())
		case "file":
			fmt.Fprintf(&sb, "%s (%d bytes)\n", e.GetName(), e.GetSize())
		default:
			fmt.Fprintf(&sb, "%s (%s)\n", e.GetName(), e.GetType())
		}
	}
	if len(entries) > len(shown) {
		fmt.Fprintf(&sb, "... (%d more entries not shown)\n", len(entries)-len(shown))
	}
	return sb.String()
}

func registerGetFileAtRefTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "get_file_at_ref",
		Description: "Read a file (or list a directory) from a GitHub repository at any branch, tag or commit, through the GitHub API. " +
			"Use it to see how a file looked on the base branch or on another branch when that ref is not checked out locally, " +
			"or to read files of another repository. Files larger than 1 MB are not available through this API. " +
			"For the local checkout use read_file or git_show.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Repository-relative file or directory path. Use '.' for the repository root.",
				},
				"ref": map[string]any{
					"type":        "string",
					"description": "Branch, tag or commit SHA. Defaults to the repository's default branch.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
				"line_start": map[string]any{
					"type":        "integer",
					"description": "1-based first line to return.",
				},
				"line_end": map[string]any{
					"type":        "integer",
					"description": "1-based last line to return (inclusive).",
				},
			},
			"required": []string{"path"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getFileAtRefArgs) (string, error) {
		filePath, err := cleanRepoPath(args.Path)
		if err != nil {
			return "", err
		}
		if args.Ref != "" {
			if err := validateGitHubRef("ref", args.Ref); err != nil {
				return "", err
			}
		}
		client, err := githubClientForTool("read files from GitHub")
		if err != nil {
			return "", err
		}
		owner, repo, err := resolveRepoTarget(ctx, rootDir, metadataFile, args.Repo)
		if err != nil {
			return "", err
		}

		refLabel := args.Ref
		if refLabel == "" {
			refLabel = "default branch"
		}
		file, dir, resp, err := client.Repositories.GetContents(ctx, owner, repo, filePath, &github.RepositoryContentGetOptions{Ref: args.Ref})
		if err != nil {
			return "", githubAPIError(fmt.Sprintf("get %s/%s:%s at %s", owner, repo, filePath, refLabel), resp, err)
		}
		if file == nil {
			return truncateOutput(formatDirectoryListing(owner, repo, filePath, refLabel, dir)), nil
		}

		header := fmt.Sprintf("# %s/%s:%s @ %s", owner, repo, filePath, refLabel)
		switch file.GetType() {
		case "symlink":
			return fmt.Sprintf("%s\n(symlink to %s)", header, file.GetTarget()), nil
		case "submodule":
			return fmt.Sprintf("%s\n(submodule: %s)", header, file.GetSubmoduleGitURL()), nil
		}
		// RepositoryContent.GetContent decodes the payload; the raw field tells whether one was sent.
		noPayload := file.Content == nil || *file.Content == ""
		if file.GetEncoding() == "none" || (noPayload && file.GetSize() > 0) {
			return "", fmt.Errorf("%s is too large for the GitHub contents API (%d bytes, limit 1 MB)", filePath, file.GetSize())
		}
		content, err := file.GetContent()
		if err != nil {
			return "", fmt.Errorf("failed to decode %s: %w", filePath, err)
		}
		if bytes.IndexByte([]byte(content), 0) >= 0 {
			return fmt.Sprintf("%s\n(binary file, %d bytes; contents not shown)", header, file.GetSize()), nil
		}

		total := strings.Count(content, "\n")
		if content != "" && !strings.HasSuffix(content, "\n") {
			total++
		}
		if args.LineStart > 0 || args.LineEnd > 0 {
			content = windowLines(content, args.LineStart, args.LineEnd)
		}
		return truncateOutput(fmt.Sprintf("%s (%d lines, %d bytes)\n%s", header, total, file.GetSize(), content)), nil
	})
}
