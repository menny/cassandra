package prompts

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed reviewer_prompt.md
var reviewerPrompt string

//go:embed code_review_main_guidelines.md
var mainGuidelines string

// BuildSystemPrompt constructs the full system prompt from base prompts, general guidelines,
// optional personal guidelines, and any relevant AGENTS.md files for the changed paths.
func BuildSystemPrompt(workspaceRoot string, changedFiles []string, mainGuidelinesOverride string) (string, error) {
	guidelines := mainGuidelines
	if mainGuidelinesOverride != "" {
		content, err := os.ReadFile(mainGuidelinesOverride)
		if err != nil {
			return "", fmt.Errorf("failed to read main guidelines override: %w", err)
		}
		guidelines = string(content)
	}

	prompt := reviewerPrompt + "\n<code_review_guidelines>\n" + guidelines + "\n</code_review_guidelines>\n"

	personalPath := filepath.Join(workspaceRoot, "scratch", "personal.ai_code_review_guidelines.md")
	if personalBytes, err := os.ReadFile(personalPath); err == nil {
		prompt += fmt.Sprintf("\n<personal_review_guidelines>\n%s\n</personal_review_guidelines>\n", string(personalBytes))
	}

	agentsMDs := findAgentsMDFiles(workspaceRoot, changedFiles)
	if len(agentsMDs) > 0 {
		prompt += "\n<agents_guidelines>\n"
		for path, content := range agentsMDs {
			prompt += fmt.Sprintf("Path: %s\n%s\n\n", path, content)
		}
		prompt += "</agents_guidelines>\n"
	}

	return prompt, nil
}

// findAgentsMDFiles walks up the directory tree for each changed file from the file's dir
// up to the workspace root, looking for AGENTS.md files.
// Returns a map of repo-relative path to file contents.
func findAgentsMDFiles(workspaceRoot string, changedFiles []string) map[string]string {
	found := make(map[string]string)

	for _, file := range changedFiles {
		absPath := filepath.Join(workspaceRoot, file)
		dir := filepath.Dir(absPath)

		for {
			agentsPath := filepath.Join(dir, "AGENTS.md")
			relKey, err := filepath.Rel(workspaceRoot, agentsPath)
			if err != nil {
				relKey = agentsPath
			}

			if _, exists := found[relKey]; !exists {
				if content, err := os.ReadFile(agentsPath); err == nil {
					found[relKey] = string(content)
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
