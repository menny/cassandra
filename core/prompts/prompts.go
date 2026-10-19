package prompts

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/menny/cassandra/util"
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
// the selected general guidelines (mainGuidelinesContent), any additive
// supplementalGuidelinesContent, any repository-specific rules found
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
//  3. <supplemental_guideline>  — semi-static, additive guidelines
//  4. <approval_evaluation_guidelines> — semi-static
//  5. <personal_review_guidelines>     — semi-static, optional
//  6. ask_developer guidelines         — semi-static, optional
//  7. <t3_thread_launch_guidelines>    — semi-static, optional (T3 Code sessions only)
//  8. <pr_comment_guidelines>          — semi-static
//
// Dynamic (Zone 3):
//  9. <agents_guidelines>       — dynamic, varies per PR (AGENTS.md files)
//  10. <reviewer_context>       — dynamic, varies per PR (REVIEWERS.md files)
func BuildSystemPrompt(workspaceRoot string, changedFiles []string, mainGuidelinesContent string, supplementalGuidelinesContent []string, approvalEvaluationContent string, allowAskDeveloper bool, allowT3ThreadLaunch bool, currentModel string) (stable, dynamic string, summary PromptSummary, err error) {
	if mainGuidelinesContent == "" {
		return "", "", summary, fmt.Errorf("main guidelines content is required")
	}

	if approvalEvaluationContent == "" {
		approvalEvaluationContent = approvalEvaluationPrompt
	}

	// Zone 1 (static) + Zone 2 (semi-static) — identical across all PRs on the same config.
	stable = reviewerPrompt + "\n<code_review_guidelines>\n" + mainGuidelinesContent + "\n</code_review_guidelines>\n"

	for _, sg := range supplementalGuidelinesContent {
		if sg != "" {
			stable += "\n<supplemental_guideline>\n" + sg + "\n</supplemental_guideline>\n"
		}
	}

	stable += "\n<approval_evaluation_guidelines>\n" + approvalEvaluationContent + "\n</approval_evaluation_guidelines>\n"

	personalPath := filepath.Join(workspaceRoot, "personal.ai_code_review_guidelines.md")
	if personalBytes, err := os.ReadFile(personalPath); err == nil {
		stable += fmt.Sprintf("\n<personal_review_guidelines>\n%s\n</personal_review_guidelines>\n", string(personalBytes))
		relPersonal := util.SafeRel(workspaceRoot, personalPath)
		summary.LoadedFiles = append(summary.LoadedFiles, FileSource{Path: relPersonal, Type: "personal"})
	}

	// Zone 2: semi-static, config-driven
	if allowAskDeveloper {
		stable += "\nYou have access to the `ask_developer` tool. If you lack context about internal business logic or are unsure about a complex architectural choice, do not guess. Call the `ask_developer` tool to clarify. If the tool returns a timeout message, do your best to assume the answer and document your assumption.\n"
	}

	if allowT3ThreadLaunch {
		currentModelText := ""
		if currentModel != "" {
			currentModelText = fmt.Sprintf("Your current active model is `%s`.\n", currentModel)
		}
		stable += fmt.Sprintf("\n<t3_thread_launch_guidelines>\n"+
			"You are running inside a T3 Code session and have access to the `t3_thread_launch` and `orchestrator_capabilities` tools.\n%s\n"+
			"1. **When to use**:\n"+
			"   - Use `t3_thread_launch` when explicitly asked by the user to fix issues or create a fix session/thread.\n"+
			"   - Proactively offer to spawn a fix session when it is clear the user wants review findings or code issues addressed. Do not spawn it without user confirmation unless explicitly instructed.\n\n"+
			"2. **Model and Provider Selection Logic**:\n"+
			"   - Before launching a thread, call `orchestrator_capabilities` to inspect the available providers and models.\n"+
			"   - Identify your current active model. Look through the `providers` list in `orchestrator_capabilities` to find a provider whose `models` contains your active model, or matches `inheritedProviderInstanceId`.\n"+
			"   - Use that provider's `providerInstanceId` as `modelSelection.instanceId`.\n"+
			"   - Pick an appropriate model from that provider instance's available options for `modelSelection.model`, choosing as light a model as possible suitable for the task (e.g., flash/haiku tier for straightforward code fixes).\n"+
			"   - If no provider matches, fall back to `inheritedProviderInstanceId` / `inheritedModel` or omit `modelSelection` to inherit current session settings.\n\n"+
			"3. **Spawned Thread Message & Context**:\n"+
			"   - The `message` parameter must contain comprehensive initial instructions for the fixing agent:\n"+
			"     a) Brief context of the review and files affected.\n"+
			"     b) The user's goals, directions, and specific items to fix.\n"+
			"     c) Technical guidelines and constraints necessary to complete the fix accurately.\n\n"+
			"4. **Thread Title**:\n"+
			"   - The thread `title` must strictly follow the format: \"Review Fix [short description]\" (e.g., \"Review Fix Refactor Auth Token Validation\").\n\n"+
			"5. **Referencing the Spawned Thread**:\n"+
			"   - When presenting the launched thread to the user, link it using markdown: `[Review Fix ...](t3-thread://v1/<threadId>)` using the exact `threadId` returned by `t3_thread_launch`.\n"+
			"</t3_thread_launch_guidelines>\n", currentModelText)
	}

	// Zone 2: PR comment guidelines
	stable += "\n<pr_comment_guidelines>\n" +
		"You have access to the `post_pr_comment` tool to publish comments on a GitHub pull request.\n\n" +
		"1. **When to use**:\n" +
		"   - Use `post_pr_comment` ONLY when explicitly asked or confirmed by the user (e.g., \"post that comment\", \"add an inline note on line 42\").\n" +
		"   - Do NOT post comments automatically or unprompted.\n\n" +
		"2. **Proactive suggestions**:\n" +
		"   - When you identify important findings, architectural questions, or suggested improvements during review or discussion, proactively offer to post them as comments:\n" +
		"     - \"Would you like me to post this finding as an inline comment on `path/to/file.go:L30-L35`?\"\n" +
		"     - \"Would you like me to post this summary as a PR comment?\"\n" +
		"   - Wait for the user's explicit consent before calling the tool.\n\n" +
		"3. **Strict scope**:\n" +
		"   - This tool ONLY posts comments (top-level PR discussion comments or inline line/range comments). It cannot approve, reject, or request changes on a pull request.\n" +
		"</pr_comment_guidelines>\n"

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
			relDir := util.SafeRel(workspaceRoot, dir)
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
