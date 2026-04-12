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
)

// stderr is used for all diagnostic / progress output so that the final review
// (written to stdout) can be cleanly captured or piped by the caller.
var stderr = log.New(os.Stderr, "", 0)

func main() {
	var base string
	var head string
	var modelName string
	var provider string
	var providerAPIKey string
	var workingDir string
	var mainGuidelines string
	var maxTokens int
	var reviewOutputFile string
	var outputJSONFile string
	var extractionModel string
	var metadataJSONFile string
	var approvalEvaluationPromptFile string
	var diffFile string
	var filesListFile string
	var commitsFile string

	flag.StringVar(&workingDir, "cwd", "", "Working directory (defaults to BUILD_WORKSPACE_DIRECTORY or current directory)")
	flag.StringVar(&mainGuidelines, "main-guidelines", "general", "Path to a file or a named prompt from the library")
	flag.StringVar(&approvalEvaluationPromptFile, "approval-evaluation-prompt-file", "", "Optional path to a file containing custom approval evaluation guidelines")
	flag.IntVar(&maxTokens, "max-tokens", 8192, "Max tokens for the LLM response")
	flag.StringVar(&base, "base", "main", "Base commit/branch for diff")
	flag.StringVar(&head, "head", "HEAD", "Head commit/branch for diff")
	flag.StringVar(&reviewOutputFile, "review-output-file", "", "Path to a file where the final review will be written")
	flag.StringVar(&outputJSONFile, "output-json", "", "Path to a file where the structured JSON review will be written")
	flag.StringVar(&extractionModel, "extraction-model", "", "Optional model override for the structured JSON extraction pass (requires --output-json)")
	flag.StringVar(&metadataJSONFile, "metadata-json", "", "Path to a JSON file containing PR metadata")
	flag.StringVar(&diffFile, "diff-file", "", "Path to a file containing the git diff")
	flag.StringVar(&filesListFile, "files-list-file", "", "Path to a file containing the list of changed files (one per line)")
	flag.StringVar(&commitsFile, "commits-file", "", "Path to a file containing the commit messages")

	flag.StringVar(&modelName, "model", "", "LLM provider's model id (e.g. gemini-3-flash-preview, claude-3-7-sonnet-20250219)")
	flag.StringVar(&provider, "provider", "", "LLM provider to use (google, anthropic)")
	flag.StringVar(&providerAPIKey, "provider-api-key", "", "API key for the selected provider (required)")

	// pflag natively errors and exits on unknown flags unless configured otherwise
	flag.Parse()

	// Error if there are dangling positional arguments
	if len(flag.Args()) > 0 {
		fmt.Printf("Error: unknown or positional arguments provided: %v\n", flag.Args())
		os.Exit(1)
	}

	// Move to the intended working directory if executing via bazel or explicitly requested
	targetDir := workingDir
	if targetDir == "" {
		targetDir = os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	}
	if targetDir != "" {
		if err := os.Chdir(targetDir); err != nil {
			log.Fatalf("Failed to change directory to %s: %v", targetDir, err)
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
		stderr.Printf("Error: missing required arguments:\n  - %s\n", strings.Join(missing, "\n  - "))
		os.Exit(1)
	}

	// Resolve main guidelines content
	mainGuidelinesContent, err := resolveMainGuidelinesContent(mainGuidelines)
	if err != nil {
		log.Fatalf("Failed to resolve main guidelines: %v", err)
	}

	var approvalEvaluationContent string
	if approvalEvaluationPromptFile != "" {
		content, err := os.ReadFile(approvalEvaluationPromptFile)
		if err != nil {
			log.Fatalf("Failed to read approval evaluation prompt file: %v", err)
		}
		approvalEvaluationContent = string(content)
	}

	stderr.Println("=== Cassandra Configuration ===")
	stderr.Printf("  Working Directory: %s\n", targetDir)
	stderr.Printf("  Base: %s\n", base)
	stderr.Printf("  Head: %s\n", head)
	stderr.Printf("  LLM Provider: %s\n", provider)
	stderr.Printf("  LLM Model: %s\n", modelName)
	if mainGuidelines != "" {
		stderr.Printf("  Main Guidelines: %s\n", mainGuidelines)
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

	ctx := context.Background()

	// Initialize LLM Client
	client, err := factory.New(ctx, provider, modelName, providerAPIKey)
	if err != nil {
		log.Fatalf("Failed to initialize LLM: %v", err)
	}

	// Initialize Agent and Tool Registry
	registry := tools.NewRegistry()
	tools.RegisterLocalTools(registry)

	agent := core.NewAgent(client, registry)

	var diffOutput string
	var changedFiles []string
	var commitsOutput string

	if diffFile != "" || filesListFile != "" {
		if diffFile == "" || filesListFile == "" {
			log.Fatal("Both --diff-file and --files-list-file must be provided together.")
		}

		diffBytes, err := os.ReadFile(diffFile)
		if err != nil {
			log.Fatalf("Failed to read diff file %s: %v", diffFile, err)
		}
		diffOutput = string(diffBytes)

		filesBytes, err := os.ReadFile(filesListFile)
		if err != nil {
			log.Fatalf("Failed to read files list file %s: %v", filesListFile, err)
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
		diffOutput, changedFiles, err = tools.FetchGitDiff(targetDir, base, head)
		if err != nil {
			log.Fatalf("Failed to extract git diff: %v", err)
		}
	}

	if commitsFile != "" {
		commitsBytes, err := os.ReadFile(commitsFile)
		if err != nil {
			log.Fatalf("Failed to read commits file %s: %v", commitsFile, err)
		}
		commitsOutput = string(commitsBytes)
	} else {
		stderr.Println("Fetching git commits locally...")
		commits, err := tools.FetchGitCommits(targetDir, base, head)
		if err != nil {
			// Don't fail if commits fetching fails (e.g. shallow clone), just log it
			stderr.Printf("Warning: failed to fetch git commits: %v\n", err)
		} else {
			commitsOutput = commits
		}
	}

	if len(changedFiles) == 0 {
		stderr.Println("No changes found to review.")
		os.Exit(0)
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

	systemPrompt, err := prompts.BuildSystemPrompt(targetDir, changedFiles, mainGuidelinesContent, approvalEvaluationContent)
	if err != nil {
		log.Fatalf("Failed to build system prompt: %v", err)
	}

	result, err := agent.RunReview(ctx, systemPrompt, requestText, maxIterations, maxTokens)
	if err != nil {
		log.Fatalf("Review failed: %v", err)
	}

	// Final review goes to stdout so it can be captured cleanly.
	fmt.Println(result)

	if reviewOutputFile != "" {
		if err := core.WriteFileWithDirs(reviewOutputFile, []byte(result)); err != nil {
			log.Fatalf("Failed to write review to %s: %v", reviewOutputFile, err)
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
			log.Fatalf("Structured extraction failed: %v", err)
		}

		// Populate the raw text manually to save tokens during extraction
		structured.RawFreeText = result

		jsonBytes, err := json.MarshalIndent(structured, "", "  ")
		if err != nil {
			log.Fatalf("Failed to marshal structured review: %v", err)
		}

		if err := core.WriteFileWithDirs(outputJSONFile, jsonBytes); err != nil {
			log.Fatalf("Failed to write structured review to %s: %v", outputJSONFile, err)
		}
		stderr.Printf("Structured review written to %s\n", outputJSONFile)
	}
}

func resolveMainGuidelinesContent(guidelinesPath string) (string, error) {
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
		sb.WriteString(fmt.Sprintf("- **Repository**: %s\n", metadata.RepoFullName))
	}
	sb.WriteString(fmt.Sprintf("- **Author**: %s\n", metadata.Author))
	sb.WriteString(fmt.Sprintf("- **Date**: %s\n", metadata.CreatedAt.Format("2006-01-02")))
	sb.WriteString(fmt.Sprintf("- **Title**: %s\n", metadata.Title))
	if metadata.Description != "" {
		sb.WriteString(fmt.Sprintf("- **Description**: %s\n", metadata.Description))
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
			sb.WriteString(fmt.Sprintf("- **%s** (%s)%s:\n", author, c.Date.Format("2006-01-02 15:04"), location))
			// Indent body and wrap in blockquote to maintain Markdown structure
			lines := strings.Split(c.Body, "\n")
			for _, line := range lines {
				sb.WriteString(fmt.Sprintf("  > %s\n", line))
			}
		}
	}

	return sb.String()
}
