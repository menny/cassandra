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

	flag.StringVar(&workingDir, "cwd", "", "Working directory (defaults to BUILD_WORKSPACE_DIRECTORY or current directory)")
	flag.StringVar(&mainGuidelines, "main-guidelines", "", "Path to a file overriding the built-in main guidelines")
	flag.IntVar(&maxTokens, "max-tokens", 8192, "Max tokens for the LLM response")
	flag.StringVar(&base, "base", "main", "Base commit/branch for diff")
	flag.StringVar(&head, "head", "HEAD", "Head commit/branch for diff")
	flag.StringVar(&reviewOutputFile, "review-output-file", "", "Path to a file where the final review will be written")
	flag.StringVar(&outputJSONFile, "output-json", "", "Path to a file where the structured JSON review will be written")
	flag.StringVar(&extractionModel, "extraction-model", "", "Optional model override for the structured JSON extraction pass (requires --output-json)")

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
		fmt.Printf("Error: missing required arguments:\n  - %s\n", strings.Join(missing, "\n  - "))
		os.Exit(1)
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

	var requestText string
	var changedFiles []string
	diffOutput, files, err := tools.FetchGitDiff(targetDir, base, head)
	if err != nil {
		log.Fatalf("Failed to extract git diff: %v", err)
	}

	if len(files) == 0 {
		stderr.Println("No changes found to review.")
		os.Exit(0)
	}

	requestText = fmt.Sprintf("Review the following git diff for issues:\n\n%s", diffOutput)
	changedFiles = files

	// Compute max ReAct iterations based on changed files.
	maxIterations := core.CalculateMaxIterations(len(changedFiles))

	systemPrompt, err := prompts.BuildSystemPrompt(targetDir, changedFiles, mainGuidelines)
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
