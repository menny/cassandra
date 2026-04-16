package prompts

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
)

//go:embed reviewer_prompt.md
var reviewerPrompt string

//go:embed extraction_prompt.md
var extractionPrompt string

//go:embed approval_evaluation_prompt.md
var approvalEvaluationPrompt string

//go:embed library/*.md
var libraryFS embed.FS

// BuildExtractionPrompt constructs the system prompt for the structured extraction pass.
func BuildExtractionPrompt() string {
	return extractionPrompt
}

// GetLibraryPrompt returns the content of a named prompt from the library.
func GetLibraryPrompt(name string) (string, error) {
	content, err := libraryFS.ReadFile(path.Join("library", name+".md"))
	if err != nil {
		return "", fmt.Errorf("prompt %q not found in library: %w", name, err)
	}
	return string(content), nil
}

// FileSource describes a file that was loaded into the system prompt.
type FileSource struct {
	// Path is the repo-relative path of the loaded file.
	Path string
	// Type describes the role of the file: "personal", "agents", or "reviewers".
	Type string
}

// PromptSummary contains metadata about a built system prompt.
type PromptSummary struct {
	// StableLen is the character length of the stable Zone 1+2 prefix.
	StableLen int
	// DynamicLen is the character length of the dynamic Zone 3 suffix.
	DynamicLen int
	// LoadedFiles lists every file that was read and incorporated into the prompt.
	LoadedFiles []FileSource
}

// BuildSystemPrompt constructs the full system prompt by combining base prompts,
// the selected general guidelines (mainGuidelinesContent), any repository-specific rules found
// in REVIEWERS.md or AGENTS.md files, and optional personal preferences from
// personal.ai_code_review_guidelines.md located in the workspace root.
//
// It returns two strings: the stable prefix (Zones 1+2) and the dynamic suffix (Zone 3),
// plus a [PromptSummary] describing the lengths and loaded files.
// The stable prefix is identical across all PRs sharing the same deployment config; the
// dynamic suffix varies per PR. Callers may concatenate them for providers that need a
// single prompt, or pass them separately to enable prompt-caching on providers that
// support it (e.g. Anthropic).
//
// Sections are ordered from most- to least-stable to maximise prefix-cache hits:
//
// Stable (Zone 1+2):
//  1. reviewerPrompt            — static, embedded at build time
//  2. <code_review_guidelines>  — semi-static, one value per deployment config
//  3. <approval_evaluation_guidelines> — semi-static
//  4. <personal_review_guidelines>     — semi-static, optional
//
// Dynamic (Zone 3):
//  5. <agents_guidelines>       — dynamic, varies per PR (AGENTS.md files)
//  6. <reviewer_context>        — dynamic, varies per PR (REVIEWERS.md files)
func BuildSystemPrompt(workspaceRoot string, changedFiles []string, mainGuidelinesContent, approvalEvaluationContent string) (stable, dynamic string, summary PromptSummary, err error) {
	if mainGuidelinesContent == "" {
		return "", "", summary, fmt.Errorf("main guidelines content is required")
	}

	if approvalEvaluationContent == "" {
		approvalEvaluationContent = approvalEvaluationPrompt
	}

	// Zone 1 (static) + Zone 2 (semi-static) — identical across all PRs on the same config.
	stable = reviewerPrompt + "\n<code_review_guidelines>\n" + mainGuidelinesContent + "\n</code_review_guidelines>\n"

	stable += "\n<approval_evaluation_guidelines>\n" + approvalEvaluationContent + "\n</approval_evaluation_guidelines>\n"

	personalPath := filepath.Join(workspaceRoot, "personal.ai_code_review_guidelines.md")
	if personalBytes, err := os.ReadFile(personalPath); err == nil {
		stable += fmt.Sprintf("\n<personal_review_guidelines>\n%s\n</personal_review_guidelines>\n", string(personalBytes))
		relPersonal, relErr := filepath.Rel(workspaceRoot, personalPath)
		if relErr != nil {
			relPersonal = personalPath
		}
		summary.LoadedFiles = append(summary.LoadedFiles, FileSource{Path: relPersonal, Type: "personal"})
	}

	// Zone 3 (dynamic) — placed last so the stable prefix above is never broken.
	agentsMDs := findRepoFiles(workspaceRoot, changedFiles, "AGENTS.md")
	if len(agentsMDs) > 0 {
		dynamic += "\n<agents_guidelines>\n"
		agentPaths := make([]string, 0, len(agentsMDs))
		for p := range agentsMDs {
			agentPaths = append(agentPaths, p)
		}
		sort.Strings(agentPaths)
		for _, p := range agentPaths {
			dynamic += fmt.Sprintf("Directory: %s\n%s\n\n", p, agentsMDs[p])
			summary.LoadedFiles = append(summary.LoadedFiles, FileSource{Path: repoRelativeFilePath(p, "AGENTS.md"), Type: "agents"})
		}
		dynamic += "</agents_guidelines>\n"
	}

	reviewersMDs := findRepoFiles(workspaceRoot, changedFiles, "REVIEWERS.md")
	if len(reviewersMDs) > 0 {
		dynamic += "\n<reviewer_context>\n"
		reviewerPaths := make([]string, 0, len(reviewersMDs))
		for p := range reviewersMDs {
			reviewerPaths = append(reviewerPaths, p)
		}
		sort.Strings(reviewerPaths)
		for _, p := range reviewerPaths {
			dynamic += fmt.Sprintf("Directory: %s\n%s\n\n", p, reviewersMDs[p])
			summary.LoadedFiles = append(summary.LoadedFiles, FileSource{Path: repoRelativeFilePath(p, "REVIEWERS.md"), Type: "reviewers"})
		}
		dynamic += "</reviewer_context>\n"
	}

	summary.StableLen = len(stable)
	summary.DynamicLen = len(dynamic)

	return stable, dynamic, summary, nil
}

// repoRelativeFilePath returns the repo-relative path for a file inside the
// directory returned by findRepoFiles. findRepoFiles uses "/" to represent the
// workspace root, so we must not join that sentinel value with filepath.Join —
// doing so would produce an absolute path like "/AGENTS.md".
func repoRelativeFilePath(dir, filename string) string {
	if dir == "/" {
		return filename
	}
	return filepath.Join(dir, filename)
}

// findRepoFiles walks up the directory tree for each changed file from the file's dir
// up to the workspace root, looking for the specified filename.
// Returns a map of repo-relative folder path to file contents.
func findRepoFiles(workspaceRoot string, changedFiles []string, filename string) map[string]string {
	found := make(map[string]string)
	searchedDirs := make(map[string]bool)

	workspaceRoot = filepath.Clean(workspaceRoot)
	for _, file := range changedFiles {
		absPath := filepath.Join(workspaceRoot, file)
		dir := filepath.Dir(absPath)

		for {
			relDir, err := filepath.Rel(workspaceRoot, dir)
			if err != nil {
				relDir = dir
			}
			if relDir == "." {
				relDir = "/"
			}

			// Unique key for (directory, filename) to avoid redundant I/O.
			cacheKey := filepath.Join(relDir, filename)
			if searchedDirs[cacheKey] {
				// If we've searched this directory before, we've also already
				// walked up to the root from here. We can stop.
				break
			}

			searchedDirs[cacheKey] = true
			targetPath := filepath.Join(dir, filename)
			if content, err := os.ReadFile(targetPath); err == nil {
				if _, exists := found[relDir]; !exists {
					found[relDir] = string(content)
				}
			}

			if dir == workspaceRoot || dir == "." || dir == "/" || dir == filepath.Dir(dir) {
				break
			}
			dir = filepath.Dir(dir)
		}
	}

	return found
}
