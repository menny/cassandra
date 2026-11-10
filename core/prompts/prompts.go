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
//  6. <ask_developer_guidelines>       — semi-static, optional
//  7. <t3_thread_launch_guidelines>    — semi-static, optional (T3 Code sessions only)
//  8. <omnigent_session_launch_guidelines> — semi-static, optional (Omnigent sessions only)
//  9. <pr_comment_guidelines>          — semi-static, optional (ACP sessions only)
//  10. <url_fetch_guidelines>           — semi-static, optional
//
// Dynamic (Zone 3):
//  11. <agents_guidelines>       — dynamic, varies per PR (AGENTS.md files)
//  12. <reviewer_context>        — dynamic, varies per PR (REVIEWERS.md files)
func BuildSystemPrompt(workspaceRoot string, changedFiles []string, mainGuidelinesContent string, supplementalGuidelinesContent []string, approvalEvaluationContent string, allowAskDeveloper bool, allowT3ThreadLaunch bool, allowOmnigentLaunch bool, allowPRComment bool, allowURLFetch bool, currentModel string, promptForHarness ...bool) (stable, dynamic string, summary PromptSummary, err error) {
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
		stable += "\n<ask_developer_guidelines>\n" +
			"You have access to the `ask_developer` tool to ask the developer clarifying questions during the review.\n\n" +
			"1. **When to ask**:\n" +
			"   - Ambiguous Intent: A change removes or significantly alters existing functionality or tests, and it is ambiguous whether this was intentional or an accidental regression.\n" +
			"   - Missing Business Context: Critical business rules, edge cases, or architectural decisions that cannot be determined from the code, git history, or PR description.\n" +
			"   - Verdict-Altering Uncertainty: Only ask questions whose answers would directly affect your review findings or concluding verdict (Approved vs Rejected vs Comment).\n\n" +
			"2. **When NOT to ask**:\n" +
			"   - Do NOT ask questions that can be answered by exploring the repository using `read_file`, `glob_files`, or `grep_files`. Always investigate first.\n" +
			"   - Do NOT ask about subjective stylistic choices, formatting, or trivial preferences.\n" +
			"   - Do NOT ask questions after you have already determined your final verdict.\n\n" +
			"3. **How to ask**:\n" +
			"   - Provide concrete, concise `options` whenever feasible, and set `is_multi_select: true` if multiple choices may apply to make answering fast and clear for the developer.\n" +
			"   - Keep questions brief and explain your `reasoning` clearly so the developer understands what hinges on their answer.\n\n" +
			"4. **Handling timeouts or skipped prompts**:\n" +
			"   - If the tool returns a timeout or skipped response, do not abort or stall the review. Proceed using your best technical judgment and explicitly state your assumption in the review finding.\n" +
			"</ask_developer_guidelines>\n"
	}

	if allowT3ThreadLaunch {
		currentModelText := ""
		if currentModel != "" {
			currentModelText = fmt.Sprintf("Your current active model is `%s`.\n", currentModel)
		}
		pForHarness := true
		if len(promptForHarness) > 0 {
			pForHarness = promptForHarness[0]
		}
		var clarificationRule string
		if pForHarness && allowAskDeveloper {
			clarificationRule = "   - **Interactive Clarification with `ask_developer`**: If the user did not specify which harness or model to use, and `ask_developer` is available, ask the user to choose from the enabled coding harnesses discovered via `orchestrator_capabilities`.\n"
		} else {
			clarificationRule = "   - **Deterministic Selection**: Do NOT prompt the developer to choose a harness. Directly omit or leave `modelSelection` to allow Cassandra's deterministic orchestrator to select the best available coding harness based on configured priority.\n"
		}
		stable += fmt.Sprintf("\n<t3_thread_launch_guidelines>\n"+
			"You are running inside a T3 Code session and have access to the `t3_thread_launch` and `orchestrator_capabilities` tools.\n%s\n"+
			"1. **When to use**:\n"+
			"   - Use `t3_thread_launch` when explicitly asked by the user to fix issues or create a fix session/thread.\n"+
			"   - Proactively offer to spawn a fix session when it is clear the user wants review findings or code issues addressed. Do not spawn it without user confirmation unless explicitly instructed.\n\n"+
			"2. **Agent Harness and Model Selection Rules**:\n"+
			"   - **NEVER select Cassandra as the fix agent**: Cassandra is exclusively a read-only code review agent and cannot edit files, run commands, or implement fixes. Its instance ID ('cassandra') must NEVER be used in `modelSelection.instanceId`.\n"+
			"   - **Target Harness Requirements**: The target must be an active coding agent harness capable of modifying files and running builds/tests (e.g., `antigravity`, `opencode`, `claudeAgent`). Filter providers from `orchestrator_capabilities`: they must have `canRunChildTask: true`, not have disabled constraints, and not be Cassandra.\n"+
			"%s"+
			"   - **Deterministic Fallback**: If the user does not specify a harness or skips clarification, omit or leave `modelSelection` to allow Cassandra's deterministic orchestrator to select the best available coding harness based on configured priority.\n"+
			"   - **Model Tier Selection**: Pick an appropriate model tier for the fix complexity (e.g. lightweight/fast tier for targeted fixes, high-reasoning for major refactors).\n\n"+
			"3. **Spawned Thread Message & Context**:\n"+
			"   - The `message` parameter must contain comprehensive initial instructions for the fixing agent:\n"+
			"     a) Brief context of the review and files affected.\n"+
			"     b) The user's goals, directions, and specific items to fix.\n"+
			"     c) Technical guidelines and constraints necessary to complete the fix accurately.\n\n"+
			"4. **Thread Title**:\n"+
			"   - The thread `title` must strictly follow the format: \"Review Fix [short description]\" (e.g., \"Review Fix Refactor Auth Token Validation\").\n\n"+
			"5. **Referencing the Spawned Thread**:\n"+
			"   - When presenting the launched thread to the user, link it using markdown: `[Review Fix ...](t3-thread://v1/<threadId>)` using the exact `threadId` returned by `t3_thread_launch`.\n"+
			"</t3_thread_launch_guidelines>\n", currentModelText, clarificationRule)
	}

	if allowOmnigentLaunch {
		currentModelText := ""
		if currentModel != "" {
			currentModelText = fmt.Sprintf("Your current active model is `%s`.\n", currentModel)
		}
		pForHarness := true
		if len(promptForHarness) > 0 {
			pForHarness = promptForHarness[0]
		}
		var clarificationRule string
		if pForHarness && allowAskDeveloper {
			clarificationRule = "   - **Interactive Clarification with `ask_developer`**: If the user did not specify which harness or model to use, and `ask_developer` is available, ask the user to choose from the enabled coding harnesses discovered via `omnigent_agent_capabilities`.\n"
		} else {
			clarificationRule = "   - **Deterministic Selection**: Do NOT prompt the developer to choose a harness. Directly omit or leave `agentId` to allow Cassandra's deterministic orchestrator to select the best available coding harness based on configured priority.\n"
		}
		stable += fmt.Sprintf("\n<omnigent_session_launch_guidelines>\n"+
			"You are running inside an Omnigent session and have access to the `omnigent_session_launch` and `omnigent_agent_capabilities` tools.\n%s\n"+
			"1. **When to use**:\n"+
			"   - Use `omnigent_session_launch` when explicitly asked by the user to fix issues or create a fix session/thread.\n"+
			"   - Proactively offer to spawn a fix session when it is clear the user wants review findings or code issues addressed. Do not spawn it without user confirmation unless explicitly instructed.\n\n"+
			"2. **Agent Harness and Model Selection Rules**:\n"+
			"   - **NEVER select Cassandra as the fix agent**: Cassandra is exclusively a read-only code review agent and cannot edit files, run commands, or implement fixes. Its ID ('cassandra') must NEVER be used in `agentId`.\n"+
			"   - **Target Harness Requirements**: The target must be an active coding agent harness capable of modifying files and running builds/tests (e.g., `antigravity-native-ui`, `opencode-native-ui`, `claude-native-ui`, `codex-native-ui`). Discovered via `omnigent_agent_capabilities`.\n"+
			"%s"+
			"   - **Deterministic Fallback**: If the user does not specify an agent or skips clarification, omit or leave `agentId` to allow Cassandra's deterministic orchestrator to select the best available coding harness based on configured priority.\n"+
			"   - **Model Tier Selection**: Pick an appropriate model tier for the fix complexity if overriding model (e.g. lightweight/fast tier for targeted fixes, high-reasoning for major refactors).\n"+
			"   - **Placement**: Defaults to \"child\" (nested in the current conversation tree). Can be overridden to \"same_host\" (independent top-level session on the same workspace) if requested by the user.\n\n"+
			"3. **Spawned Session Message & Context**:\n"+
			"   - The `message` parameter must contain comprehensive initial instructions for the fixing agent:\n"+
			"     a) Brief context of the review and files affected.\n"+
			"     b) The user's goals, directions, and specific items to fix.\n"+
			"     c) Technical guidelines and constraints necessary to complete the fix accurately.\n\n"+
			"4. **Session Title**:\n"+
			"   - The session `title` must strictly follow the format: \"Review Fix [short description]\" (e.g., \"Review Fix Refactor Auth Token Validation\").\n\n"+
			"5. **Referencing the Spawned Session**:\n"+
			"   - When presenting the launched session to the user, link it using markdown: `[Review Fix ...](omnigent-session://<conversationId>)` using the exact `conversationId` returned by `omnigent_session_launch`.\n"+
			"</omnigent_session_launch_guidelines>\n", currentModelText, clarificationRule)
	}

	// Zone 2: PR comment guidelines (ACP sessions only)
	if allowPRComment {
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
	}

	if allowURLFetch {
		stable += "\n<url_fetch_guidelines>\n" +
			"You have access to the `mcp-server-fetch_fetch` tool to retrieve content from external URLs.\n\n" +
			"1. **When to use**:\n" +
			"   - When the diff, code comments, or PR description reference an external documentation URL to justify an API endpoint, method signature, payload structure, or behavior, you MUST use `mcp-server-fetch_fetch` to retrieve and verify that URL rather than relying on internal training data or skipping verification.\n" +
			"   - If the fetched content is truncated and the relevant section has not yet appeared, call `mcp-server-fetch_fetch` again with `start_index` to read subsequent chunks.\n\n" +
			"2. **When NOT to use**:\n" +
			"   - Do NOT fetch arbitrary or unrelated URLs (e.g., generic license headers, homepage links, or standard library references that do not require external verification).\n" +
			"</url_fetch_guidelines>\n"
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
