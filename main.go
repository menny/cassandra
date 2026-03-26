package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/llmutil"
	"github.com/menny/cassandra/tools"
)

func main() {
	var diffBranch string
	var prNumber int
	var modelName string
	var provider string
	var providerAPIKey string
	var workingDir string
	var mainGuidelines string

	flag.StringVar(&workingDir, "cwd", "", "Working directory (defaults to BUILD_WORKSPACE_DIRECTORY or current directory)")
	flag.StringVar(&mainGuidelines, "main_guidelines", "", "Path to a file overriding the built-in main guidelines")
	flag.StringVar(&diffBranch, "diff", "", "Review git diff against the specified branch (default 'main')")
	flag.Lookup("diff").NoOptDefVal = "main" // Allows omitting the value and defaulting to 'main'

	flag.IntVar(&prNumber, "pr", 0, "Review a GitHub PR by specifying its number")
	flag.StringVar(&modelName, "model", "", "LLM provider's model id (e.g. gemini-1.5-pro, claude-3-5-sonnet-20241022)")
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
	if diffBranch == "" && prNumber == 0 {
		missing = append(missing, "either --diff or --pr")
	}
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

	ctx := context.Background()

	// Initialize LLM Client
	client, err := llmutil.NewClient(ctx, provider, modelName, providerAPIKey)
	if err != nil {
		log.Fatalf("Failed to initialize LLM: %v", err)
	}

	// Initialize Agent and Tool Registry
	registry := tools.NewRegistry()
	if prNumber != 0 {
		tools.RegisterPRTools(registry, prNumber)
	} else {
		tools.RegisterLocalTools(registry)
	}

	agent := core.NewAgent(client, registry)

	var requestText string
	if diffBranch != "" || flag.Lookup("diff").Changed {
		diffOutput, err := tools.FetchGitDiff(diffBranch)
		if err != nil {
			log.Fatalf("Failed to extract git diff: %v", err)
		}
		requestText = fmt.Sprintf("Review the following git diff for issues:\n\n%s", diffOutput)
	} else {
		requestText = "Review the provided changes for issues."
	}

	result, err := agent.RunReview(ctx, requestText)
	if err != nil {
		log.Fatalf("Review failed: %v", err)
	}

	fmt.Println("=== AI Review ===")
	fmt.Println(result)
}
