package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/factory"
	"github.com/menny/cassandra/tools"
	"github.com/menny/cassandra/tools/mcp"
)

func main() {
	ctx := context.Background()
	stderr := log.New(os.Stderr, "", 0)
	if err := run(ctx, stderr); err != nil {
		stderr.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, stderr *log.Logger) error {
	var base string
	var head string
	var modelName string
	var provider string
	var providerAPIKey string
	var providerURL string
	var workingDir string
	var mainGuidelines string
	var supplementalGuidelines []string
	var maxTokens int
	var reviewOutputFile string
	var outputJSONFile string
	var extractionModel string
	var metadataJSONFile string
	var approvalEvaluationPromptFile string
	var diffFile string
	var filesListFile string
	var commitsFile string
	var mcpConfigFile string

	flag.StringVar(&workingDir, "cwd", "", "Working directory (defaults to BUILD_WORKSPACE_DIRECTORY or current directory)")
	flag.StringVar(&mainGuidelines, "main-guidelines", "general", "Path to a file or a named prompt from the library")
	flag.StringArrayVar(&supplementalGuidelines, "supplemental-guidelines", nil, "Optional additive paths or named library prompts for supplemental guidelines (can be used multiple times)")
	flag.StringVar(&approvalEvaluationPromptFile, "approval-evaluation-prompt-file", "", "Optional path to a file containing custom approval evaluation guidelines")
	flag.IntVar(&maxTokens, "max-tokens", llm.DefaultMaxTokens, "Max tokens for the LLM response")
	flag.StringVar(&base, "base", "main", "Base commit/branch for diff")
	flag.StringVar(&head, "head", "HEAD", "Head commit/branch for diff")
	flag.StringVar(&reviewOutputFile, "review-output-file", "", "Path to a file where the final review will be written")
	flag.StringVar(&outputJSONFile, "output-json", "", "Path to a file where the structured JSON review will be written")
	flag.StringVar(&extractionModel, "extraction-model", "", "Optional model override for the structured JSON extraction pass (requires --output-json)")
	flag.StringVar(&metadataJSONFile, "metadata-json", "", "Path to a JSON file containing PR metadata")
	flag.StringVar(&diffFile, "diff-file", "", "Path to a file containing the git diff")
	flag.StringVar(&filesListFile, "files-list-file", "", "Path to a file containing the list of changed files (one per line)")
	flag.StringVar(&commitsFile, "commits-file", "", "Path to a file containing the commit messages")
	flag.StringVar(&mcpConfigFile, "mcp-config", "", "Path to an mcp.json file configuring custom tools for the reviewer")

	flag.StringVar(&modelName, "model", "", "LLM provider's model id (e.g. gemini-3-flash-preview, claude-3-7-sonnet-20250219)")
	flag.StringVar(&provider, "provider", "", "LLM provider to use (google, anthropic)")
	flag.StringVar(&providerAPIKey, "provider-api-key", "", "API key for the selected provider (required)")
	flag.StringVar(&providerURL, "provider-url", "", "Optional API endpoint URL override (e.g. for OpenAI-compatible providers like Ollama)")

	// pflag natively errors and exits on unknown flags unless configured otherwise
	flag.Parse()

	// Error if there are dangling positional arguments
	if len(flag.Args()) > 0 {
		return fmt.Errorf("unknown or positional arguments provided: %v", flag.Args())
	}

	// Move to the intended working directory if executing via bazel or explicitly requested
	targetDir := workingDir
	if targetDir == "" {
		targetDir = os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	}
	if targetDir != "" {
		if err := os.Chdir(targetDir); err != nil {
			return fmt.Errorf("failed to change directory to %s: %w", targetDir, err)
		}
	}

	var missing []string
	if provider == "" {
		missing = append(missing, "--provider")
	}
	if modelName == "" {
		missing = append(missing, "--model")
	}
	if providerAPIKey == "" {
		missing = append(missing, "--provider-api-key")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required arguments:\n  - %s", strings.Join(missing, "\n  - "))
	}

	// Resolve main guidelines content
	mainGuidelinesContent, err := resolveGuidelinesContent(mainGuidelines)
	if err != nil {
		return fmt.Errorf("failed to resolve main guidelines: %w", err)
	}

	var supplementalGuidelinesContent []string
	for _, sg := range supplementalGuidelines {
		content, err := resolveGuidelinesContent(sg)
		if err != nil {
			return fmt.Errorf("failed to resolve supplemental guideline %q: %w", sg, err)
		}
		supplementalGuidelinesContent = append(supplementalGuidelinesContent, content)
	}

	var approvalEvaluationContent string
	if approvalEvaluationPromptFile != "" {
		content, err := os.ReadFile(approvalEvaluationPromptFile)
		if err != nil {
			return fmt.Errorf("failed to read approval evaluation prompt file: %w", err)
		}
		approvalEvaluationContent = string(content)
	}

	stderr.Println("=== Cassandra Configuration ===")
	stderr.Printf("  Working Directory: %s\n", targetDir)
	stderr.Printf("  Base: %s\n", base)
	stderr.Printf("  Head: %s\n", head)
	stderr.Printf("  LLM Provider: %s\n", provider)
	stderr.Printf("  LLM Model: %s\n", modelName)
	if providerURL != "" {
		stderr.Printf("  LLM Provider URL: %s\n", providerURL)
	}
	if mainGuidelines != "" {
		stderr.Printf("  Main Guidelines: %s\n", mainGuidelines)
	}
	if len(supplementalGuidelines) > 0 {
		stderr.Printf("  Supplemental Guidelines: %s\n", strings.Join(supplementalGuidelines, ", "))
	}
	if outputJSONFile != "" {
		stderr.Printf("  Structured Output JSON: %s\n", outputJSONFile)
		if extractionModel != "" {
			stderr.Printf("  Extraction Model: %s\n", extractionModel)
		}
	}
	if metadataJSONFile != "" {
		stderr.Printf("  Metadata JSON: %s\n", metadataJSONFile)
	}
	if approvalEvaluationPromptFile != "" {
		stderr.Printf("  Approval Evaluation Prompt File: %s\n", approvalEvaluationPromptFile)
	}
	stderr.Println("  API Key: [PROVIDED]")
	stderr.Println("===============================")

	// Initialize LLM Client
	client, err := factory.New(ctx, provider, modelName, providerAPIKey, providerURL)
	if err != nil {
		return fmt.Errorf("failed to initialize LLM: %w", err)
	}

	// Initialize Agent and Tool Registry
	registry := tools.NewRegistry()
	tools.RegisterLocalTools(registry)

	if mcpConfigFile != "" {
		mcpData, err := os.ReadFile(mcpConfigFile)
		if err != nil {
			return fmt.Errorf("failed to read MCP config file %s: %w", mcpConfigFile, err)
		}
		var mcpConfig mcp.Config
		if err := json.Unmarshal(mcpData, &mcpConfig); err != nil {
			return fmt.Errorf("failed to parse MCP config file %s: %w", mcpConfigFile, err)
		}
		mcpConfig.ExpandEnv()

		mcpManager := mcp.NewManager()
		defer func() {
			if err := mcpManager.Close(); err != nil {
				stderr.Printf("Warning: failed to close MCP manager: %v\n", err)
			}
		}()

		stderr.Printf("Initializing MCP servers from %s...\n", mcpConfigFile)
		if err := mcpManager.RegisterServers(ctx, mcpConfig, func(def llm.ToolDef, handler func(context.Context, llm.ToolCall) (string, error)) {
			registry.RegisterTool(def, handler)
		}); err != nil {
			return fmt.Errorf("failed to register MCP servers: %w", err)
		}
	}

	agent := core.NewAgent(client, registry)

	var diffOutput string
	var changedFiles []string
	var commitsOutput string

	if diffFile != "" || filesListFile != "" {
		if diffFile == "" || filesListFile == "" {
			return fmt.Errorf("both --diff-file and --files-list-file must be provided together")
		}

		diffBytes, err := os.ReadFile(diffFile)
		if err != nil {
			return fmt.Errorf("failed to read diff file %s: %w", diffFile, err)
		}
		diffOutput = string(diffBytes)

		filesBytes, err := os.ReadFile(filesListFile)
		if err != nil {
			return fmt.Errorf("failed to read files list file %s: %w", filesListFile, err)
		}
		lines := strings.Split(strings.TrimSpace(string(filesBytes)), "\n")
		for _, line := range lines {
			if f := strings.TrimSpace(line); f != "" {
				changedFiles = append(changedFiles, f)
			}
		}
	} else {
		stderr.Println("Fetching git diff locally...")
		var err error
		diffOutput, changedFiles, err = tools.FetchGitDiff(ctx, targetDir, base, head)
		if err != nil {
			return fmt.Errorf("failed to extract git diff: %w", err)
		}
	}

	if commitsFile != "" {
		commitsBytes, err := os.ReadFile(commitsFile)
		if err != nil {
			return fmt.Errorf("failed to read commits file %s: %w", commitsFile, err)
		}
		commitsOutput = string(commitsBytes)
	} else {
		stderr.Println("Fetching git commits locally...")
		commits, err := tools.FetchGitCommits(ctx, targetDir, base, head)
		if err != nil {
			// Don't fail if commits fetching fails (e.g. shallow clone), just log it
			stderr.Printf("Warning: failed to fetch git commits: %v\n", err)
		} else {
			commitsOutput = commits
		}
	}

	if len(changedFiles) == 0 {
		stderr.Println("No changes found to review.")
		return nil
	}

	var requestTextBuilder strings.Builder
	if commitsOutput != "" {
		requestTextBuilder.WriteString("### Commit Messages\n")
		requestTextBuilder.WriteString(commitsOutput)
		requestTextBuilder.WriteString("\n\n")
	}
	requestTextBuilder.WriteString("Review the following git diff for issues:\n\n")
	requestTextBuilder.WriteString(diffOutput)

	requestText := requestTextBuilder.String()

	if metadataJSONFile != "" {
		metadataBytes, err := os.ReadFile(metadataJSONFile)
		if err != nil {
			stderr.Printf("Warning: failed to read metadata JSON: %v. Proceeding without metadata.\n", err)
		} else {
			var metadata core.PRMetadata
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				stderr.Printf("Warning: failed to parse metadata JSON: %v. Proceeding without metadata.\n", err)
			} else {
				requestText = formatMetadata(metadata) + "\n\n" + requestText
			}
		}
	}

	// Compute max ReAct iterations based on changed files.
	maxIterations := core.CalculateMaxIterations(len(changedFiles))

	systemStable, systemDynamic, promptSummary, err := prompts.BuildSystemPrompt(targetDir, changedFiles, mainGuidelinesContent, supplementalGuidelinesContent, approvalEvaluationContent)
	if err != nil {
		return fmt.Errorf("failed to build system prompt: %w", err)
	}

	stderr.Println("=== Prompt Summary ===")
	stderr.Printf("  Stable zone:  %d chars\n", promptSummary.StableLen)
	stderr.Printf("  Dynamic zone: %d chars\n", promptSummary.DynamicLen)
	stderr.Printf("  Total:        %d chars\n", promptSummary.StableLen+promptSummary.DynamicLen)
	if len(promptSummary.LoadedFiles) > 0 {
		for _, f := range promptSummary.LoadedFiles {
			stderr.Printf("  [%s] %s\n", f.Type, f.Path)
		}
	} else {
		stderr.Println("  No additional files loaded.")
	}
	stderr.Println("======================")

	result, err := agent.RunReview(ctx, systemStable, systemDynamic, requestText, maxIterations, maxTokens)
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	// Final review goes to stdout so it can be captured cleanly.
	fmt.Println(result)

	if reviewOutputFile != "" {
		if err := core.WriteFileWithDirs(reviewOutputFile, []byte(result)); err != nil {
			return fmt.Errorf("failed to write review to %s: %w", reviewOutputFile, err)
		}
		stderr.Printf("Review written to %s\n", reviewOutputFile)
	}

	if outputJSONFile != "" {
		extractionPrompt := prompts.BuildExtractionPrompt()
		structured, err := agent.ExtractStructuredReview(ctx, extractionPrompt, result, llm.StructuredConfig{
			ModelOverride: extractionModel,
			MaxTokens:     maxTokens,
		})
		if err != nil {
			return fmt.Errorf("structured extraction failed: %w", err)
		}

		// Populate the raw text manually to save tokens during extraction
		structured.RawFreeText = result

		jsonBytes, err := json.MarshalIndent(structured, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal structured review: %w", err)
		}

		if err := core.WriteFileWithDirs(outputJSONFile, jsonBytes); err != nil {
			return fmt.Errorf("failed to write structured review to %s: %w", outputJSONFile, err)
		}
		stderr.Printf("Structured review written to %s\n", outputJSONFile)
	}

	return nil
}

func resolveGuidelinesContent(guidelinesPath string) (string, error) {
	// Try the path as provided first
	if content, err := os.ReadFile(guidelinesPath); err == nil {
		return string(content), nil
	}

	// Try as a named prompt in the library (embedded)
	return prompts.GetLibraryPrompt(guidelinesPath)
}

func formatMetadata(metadata core.PRMetadata) string {
	var sb strings.Builder
	sb.WriteString("### PR Metadata\n")
	if metadata.RepoFullName != "" {
		fmt.Fprintf(&sb, "- **Repository**: %s\n", metadata.RepoFullName)
	}
	fmt.Fprintf(&sb, "- **Author**: %s\n", metadata.Author)
	fmt.Fprintf(&sb, "- **Date**: %s\n", metadata.CreatedAt.Format("2006-01-02"))
	fmt.Fprintf(&sb, "- **Title**: %s\n", metadata.Title)
	if metadata.Description != "" {
		fmt.Fprintf(&sb, "- **Description**: %s\n", metadata.Description)
	}

	if len(metadata.Comments) > 0 {
		sb.WriteString("\n### PR Comments\n")
		for _, c := range metadata.Comments {
			author := c.Author
			if c.IsSelf {
				author = fmt.Sprintf("%s (Cassandra Bot)", author)
			}
			location := ""
			if c.Path != "" {
				if c.Line > 0 {
					if c.StartLine > 0 && c.StartLine != c.Line {
						location = fmt.Sprintf(" on %s:%d-%d", c.Path, c.StartLine, c.Line)
					} else {
						location = fmt.Sprintf(" on %s:%d", c.Path, c.Line)
					}
				} else {
					location = fmt.Sprintf(" on %s (file-level)", c.Path)
				}
			}
			fmt.Fprintf(&sb, "- **%s** (%s)%s:\n", author, c.Date.Format("2006-01-02 15:04"), location)
			// Indent body and wrap in blockquote to maintain Markdown structure
			lines := strings.Split(c.Body, "\n")
			for _, line := range lines {
				fmt.Fprintf(&sb, "  > %s\n", line)
			}
		}
	}

	return sb.String()
}
