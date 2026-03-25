package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/mennyevendanan/cassandra/core"
	"github.com/mennyevendanan/cassandra/llmutil"
	"github.com/mennyevendanan/cassandra/tools"
)

func main() {
	var diffBranch string
	var prNumber int
	var modelName string
	var provider string
	var providerAPIKey string

	flag.StringVar(&diffBranch, "diff", "", "Review git diff against the specified branch")
	flag.IntVar(&prNumber, "pr", 0, "Review a GitHub PR by specifying its number")
	flag.StringVar(&modelName, "model", "", "LLM provider's model id (e.g. gemini-1.5-pro, claude-3-5-sonnet-20241022)")
	flag.StringVar(&provider, "provider", "", "LLM provider to use (google, anthropic)")
	flag.StringVar(&providerAPIKey, "provider-api-key", "", "API key for the selected provider (required)")
	flag.Parse()

	if diffBranch == "" && prNumber == 0 {
		fmt.Println("Usage: ai-review-agent --diff [branch] OR --pr [number]")
		os.Exit(1)
	}

	if provider == "" || providerAPIKey == "" || modelName == "" {
		fmt.Println("Error: --provider, --model, and --provider-api-key are required.")
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
		tools.RegisterLocalTools(registry, diffBranch)
	}

	agent := core.NewAgent(client, registry)

	fmt.Printf("Starting AI Review using model: %s\n", modelName)
	
	// Create request
	requestText := "Review the provided diff changes for issues." // Simplified
	result, err := agent.RunReview(ctx, requestText)
	if err != nil {
		log.Fatalf("Review failed: %v", err)
	}

	fmt.Println("=== AI Review ===")
	fmt.Println(result)
}
