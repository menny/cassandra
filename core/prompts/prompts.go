package prompts

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed reviewer_prompt.md
var reviewerPrompt string

//go:embed extraction_prompt.md
var extractionPrompt string

//go:embed library/*.md
var libraryFS embed.FS

// BuildExtractionPrompt constructs the system prompt for the structured extraction pass.
func BuildExtractionPrompt() string {
	return extractionPrompt
}

// GetLibraryPrompt returns the content of a named prompt from the library.
func GetLibraryPrompt(name string) (string, error) {
	content, err := libraryFS.ReadFile(filepath.Join("library", name+".md"))
	if err != nil {
		return "", fmt.Errorf("prompt %q not found in library: %w", name, err)
	}
	return string(content), nil
}

// BuildSystemPrompt constructs the full system prompt from base prompts, general guidelines,
// optional personal guidelines, and any relevant AGENTS.md files for the changed paths.
func BuildSystemPrompt(workspaceRoot string, changedFiles []string, mainGuidelinesContent string) (string, error) {
	if mainGuidelinesContent == "" {
		return "", fmt.Errorf("main guidelines content is required")
	}

	prompt := reviewerPrompt + "\n<code_review_guidelines>\n" + mainGuidelinesContent

	reviewersMDs := findRepoFiles(workspaceRoot, changedFiles, "REVIEWERS.md")
	if len(reviewersMDs) > 0 {
		prompt += "\n\n# Reviewers Guidelines\n"
		for path, content := range reviewersMDs {
			prompt += fmt.Sprintf("\nDirectory: %s\n%s\n", path, content)
		}
	}
	prompt += "\n</code_review_guidelines>\n"

	personalPath := filepath.Join(workspaceRoot, "personal.ai_code_review_guidelines.md")
	if personalBytes, err := os.ReadFile(personalPath); err == nil {
		prompt += fmt.Sprintf("\n<personal_review_guidelines>\n%s\n</personal_review_guidelines>\n", string(personalBytes))
	}

	agentsMDs := findRepoFiles(workspaceRoot, changedFiles, "AGENTS.md")
	if len(agentsMDs) > 0 {
		prompt += "\n<agents_guidelines>\n"
		for path, content := range agentsMDs {
			prompt += fmt.Sprintf("Directory: %s\n%s\n\n", path, content)
		}
		prompt += "</agents_guidelines>\n"
	}

	return prompt, nil
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
