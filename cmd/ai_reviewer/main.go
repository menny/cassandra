package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/factory"
	"github.com/menny/cassandra/tools"
	"github.com/menny/cassandra/tools/mcp"
)

type config struct {
	Base                         string   `mapstructure:"base"`
	Head                         string   `mapstructure:"head"`
	Model                        string   `mapstructure:"model"`
	Provider                     string   `mapstructure:"provider"`
	ProviderAPIKey               string   `mapstructure:"provider-api-key"`
	ProviderURL                  string   `mapstructure:"provider-url"`
	WorkingDir                   string   `mapstructure:"cwd"`
	MainGuidelines               string   `mapstructure:"main-guidelines"`
	SupplementalGuidelines       []string `mapstructure:"supplemental-guidelines"`
	MaxTokens                    int      `mapstructure:"max-tokens"`
	ReviewOutputFile             string   `mapstructure:"review-output-file"`
	OutputJSONFile               string   `mapstructure:"output-json"`
	ExtractionModel              string   `mapstructure:"extraction-model"`
	MetadataJSONFile             string   `mapstructure:"metadata-json"`
	ApprovalEvaluationPromptFile string   `mapstructure:"approval-evaluation-prompt-file"`
	DiffFile                     string   `mapstructure:"diff-file"`
	FilesListFile                string   `mapstructure:"files-list-file"`
	CommitsFile                  string   `mapstructure:"commits-file"`
	MCPConfigFile                string   `mapstructure:"mcp-config"`
	ConfigFile                   string   `mapstructure:"config"`
}

func main() {
	ctx := context.Background()
	stderr := log.New(os.Stderr, "", 0)
	if err := run(ctx, stderr); err != nil {
		stderr.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, stderr *log.Logger) error {
	var cfg config

	flag.StringVar(&cfg.WorkingDir, "cwd", "", "Working directory (defaults to BUILD_WORKSPACE_DIRECTORY or current directory)")
	flag.StringVar(&cfg.MainGuidelines, "main-guidelines", "", "Path to a file or a named prompt from the library (defaults to 'general')")
	flag.StringArrayVar(&cfg.SupplementalGuidelines, "supplemental-guidelines", nil, "Optional additive paths or named library prompts for supplemental guidelines (can be used multiple times)")
	flag.StringVar(&cfg.ApprovalEvaluationPromptFile, "approval-evaluation-prompt-file", "", "Optional path to a file containing custom approval evaluation guidelines")
	flag.IntVar(&cfg.MaxTokens, "max-tokens", 0, "Max tokens for the LLM response (defaults to provider specific default)")
	flag.StringVar(&cfg.Base, "base", "", "Base commit/branch for diff (defaults to 'main')")
	flag.StringVar(&cfg.Head, "head", "", "Head commit/branch for diff (defaults to 'HEAD')")
	flag.StringVar(&cfg.ReviewOutputFile, "review-output-file", "", "Path to a file where the final review will be written")
	flag.StringVar(&cfg.OutputJSONFile, "output-json", "", "Path to a file where the structured JSON review will be written")
	flag.StringVar(&cfg.ExtractionModel, "extraction-model", "", "Optional model override for the structured JSON extraction pass (requires --output-json)")
	flag.StringVar(&cfg.MetadataJSONFile, "metadata-json", "", "Path to a JSON file containing PR metadata")
	flag.StringVar(&cfg.DiffFile, "diff-file", "", "Path to a file containing the git diff")
	flag.StringVar(&cfg.FilesListFile, "files-list-file", "", "Path to a file containing the list of changed files (one per line)")
	flag.StringVar(&cfg.CommitsFile, "commits-file", "", "Path to a file containing the commit messages")
	flag.StringVar(&cfg.MCPConfigFile, "mcp-config", "", "Path to an mcp.json file configuring custom tools for the reviewer")
	flag.StringVar(&cfg.ConfigFile, "config", "", "Path to a configuration file (toml)")

	flag.StringVar(&cfg.Model, "model", "", "LLM provider's model id (e.g. gemini-3-flash-preview, claude-3-7-sonnet-20250219)")
	flag.StringVar(&cfg.Provider, "provider", "", "LLM provider to use (google, anthropic, openai)")
	flag.StringVar(&cfg.ProviderAPIKey, "provider-api-key", "", "API key for the selected provider (required)")
	flag.StringVar(&cfg.ProviderURL, "provider-url", "", "Optional API endpoint URL override (e.g. for OpenAI-compatible providers like Ollama)")

	// pflag natively errors and exits on unknown flags unless configured otherwise
	flag.Parse()

	// Error if there are dangling positional arguments
	if len(flag.Args()) > 0 {
		return fmt.Errorf("unknown or positional arguments provided: %v", flag.Args())
	}

	// Move to the intended working directory if executing via bazel or explicitly requested
	targetDir := cfg.WorkingDir
	if targetDir == "" {
		targetDir = os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	}
	if targetDir != "" {
		if err := os.Chdir(targetDir); err != nil {
			return fmt.Errorf("failed to change directory to %s: %w", targetDir, err)
		}
	}

	v := viper.New()
	// Note: We set defaults in viper instead of pflag.
	// We only bind flags that were explicitly set by the user (Changed).
	// This ensures that pflag's zero-value defaults do not override
	// values from the configuration file or viper's own defaults.
	v.SetDefault("main-guidelines", "general")
	v.SetDefault("base", "main")
	v.SetDefault("head", "HEAD")
	v.SetDefault("max-tokens", llm.DefaultMaxTokens)
	v.SetDefault("provider", "google")
	v.SetDefault("model", "gemini-3-flash-preview")

	flag.VisitAll(func(f *flag.Flag) {
		if f.Changed {
			if err := v.BindPFlag(f.Name, f); err != nil {
				// This is highly unlikely to fail as we are iterating over our own flags
				stderr.Printf("Warning: failed to bind flag %s to viper: %v\n", f.Name, err)
			}
		}
	})

	if cfg.ConfigFile != "" {
		v.SetConfigFile(cfg.ConfigFile)
	} else {
		v.SetConfigName("cassandra")
		v.SetConfigType("toml")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		if cfg.ConfigFile != "" {
			return fmt.Errorf("config file %q not found: %w", cfg.ConfigFile, err)
		}
	}

	// Sync flag variables with viper (precedence: CLI > Config > Defaults)
	if err := v.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	var missing []string
	if cfg.Provider == "" {
		missing = append(missing, "--provider")
	}
	if cfg.Model == "" {
		missing = append(missing, "--model")
	}
	if cfg.ProviderAPIKey == "" {
		missing = append(missing, "--provider-api-key")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required arguments:\n  - %s", strings.Join(missing, "\n  - "))
	}

	// Resolve main guidelines content
	mainGuidelinesContent, err := resolveGuidelinesContent(cfg.MainGuidelines)
	if err != nil {
		return fmt.Errorf("failed to resolve main guidelines: %w", err)
	}

	var supplementalGuidelinesContent []string
	for _, sg := range cfg.SupplementalGuidelines {
		content, err := resolveGuidelinesContent(sg)
		if err != nil {
			return fmt.Errorf("failed to resolve supplemental guideline %q: %w", sg, err)
		}
		supplementalGuidelinesContent = append(supplementalGuidelinesContent, content)
	}

	var approvalEvaluationContent string
	if cfg.ApprovalEvaluationPromptFile != "" {
		content, err := os.ReadFile(cfg.ApprovalEvaluationPromptFile)
		if err != nil {
			return fmt.Errorf("failed to read approval evaluation prompt file: %w", err)
		}
		approvalEvaluationContent = string(content)
	}

	stderr.Println("=== Cassandra Configuration ===")
	stderr.Printf("  Working Directory: %s\n", targetDir)
	stderr.Printf("  Base: %s\n", cfg.Base)
	stderr.Printf("  Head: %s\n", cfg.Head)
	stderr.Printf("  LLM Provider: %s\n", cfg.Provider)
	stderr.Printf("  LLM Model: %s\n", cfg.Model)
	if cfg.ProviderURL != "" {
		stderr.Printf("  LLM Provider URL: %s\n", cfg.ProviderURL)
	}
	stderr.Printf("  Max Tokens: %d\n", cfg.MaxTokens)
	if cfg.MainGuidelines != "" {
		stderr.Printf("  Main Guidelines: %s\n", cfg.MainGuidelines)
	}
	if len(cfg.SupplementalGuidelines) > 0 {
		stderr.Printf("  Supplemental Guidelines: %s\n", strings.Join(cfg.SupplementalGuidelines, ", "))
	}
	if cfg.OutputJSONFile != "" {
		stderr.Printf("  Structured Output JSON: %s\n", cfg.OutputJSONFile)
		if cfg.ExtractionModel != "" {
			stderr.Printf("  Extraction Model: %s\n", cfg.ExtractionModel)
		}
	}
	if cfg.MetadataJSONFile != "" {
		stderr.Printf("  Metadata JSON: %s\n", cfg.MetadataJSONFile)
	}
	if cfg.ApprovalEvaluationPromptFile != "" {
		stderr.Printf("  Approval Evaluation Prompt File: %s\n", cfg.ApprovalEvaluationPromptFile)
	}
	stderr.Println("  API Key: [PROVIDED]")
	stderr.Println("===============================")

	// Initialize LLM Client
	client, err := factory.New(ctx, cfg.Provider, cfg.Model, cfg.ProviderAPIKey, cfg.ProviderURL)
	if err != nil {
		return fmt.Errorf("failed to initialize LLM: %w", err)
	}

	// Initialize Agent and Tool Registry
	registry := tools.NewRegistry()
	tools.RegisterLocalTools(registry)

	if cfg.MCPConfigFile != "" {
		mcpData, err := os.ReadFile(cfg.MCPConfigFile)
		if err != nil {
			return fmt.Errorf("failed to read MCP config file %s: %w", cfg.MCPConfigFile, err)
		}
		var mcpConfig mcp.Config
		if err := json.Unmarshal(mcpData, &mcpConfig); err != nil {
			return fmt.Errorf("failed to parse MCP config file %s: %w", cfg.MCPConfigFile, err)
		}
		mcpConfig.ExpandEnv()

		mcpManager := mcp.NewManager()
		defer func() {
			if err := mcpManager.Close(); err != nil {
				stderr.Printf("Warning: failed to close MCP manager: %v\n", err)
			}
		}()

		stderr.Printf("Initializing MCP servers from %s...\n", cfg.MCPConfigFile)
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

	if cfg.DiffFile != "" || cfg.FilesListFile != "" {
		if cfg.DiffFile == "" || cfg.FilesListFile == "" {
			return fmt.Errorf("both --diff-file and --files-list-file must be provided together")
		}

		diffBytes, err := os.ReadFile(cfg.DiffFile)
		if err != nil {
			return fmt.Errorf("failed to read diff file %s: %w", cfg.DiffFile, err)
		}
		diffOutput = string(diffBytes)

		filesBytes, err := os.ReadFile(cfg.FilesListFile)
		if err != nil {
			return fmt.Errorf("failed to read files list file %s: %w", cfg.FilesListFile, err)
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
		diffOutput, changedFiles, err = tools.FetchGitDiff(ctx, targetDir, cfg.Base, cfg.Head)
		if err != nil {
			return fmt.Errorf("failed to extract git diff: %w", err)
		}
	}

	if cfg.CommitsFile != "" {
		commitsBytes, err := os.ReadFile(cfg.CommitsFile)
		if err != nil {
			return fmt.Errorf("failed to read commits file %s: %w", cfg.CommitsFile, err)
		}
		commitsOutput = string(commitsBytes)
	} else {
		stderr.Println("Fetching git commits locally...")
		commits, err := tools.FetchGitCommits(ctx, targetDir, cfg.Base, cfg.Head)
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

	if cfg.MetadataJSONFile != "" {
		metadataBytes, err := os.ReadFile(cfg.MetadataJSONFile)
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

	result, err := agent.RunReview(ctx, systemStable, systemDynamic, requestText, maxIterations, cfg.MaxTokens)
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	// Final review goes to stdout so it can be captured cleanly.
	fmt.Println(result)

	if cfg.ReviewOutputFile != "" {
		if err := core.WriteFileWithDirs(cfg.ReviewOutputFile, []byte(result)); err != nil {
			return fmt.Errorf("failed to write review to %s: %w", cfg.ReviewOutputFile, err)
		}
		stderr.Printf("Review written to %s\n", cfg.ReviewOutputFile)
	}

	if cfg.OutputJSONFile != "" {
		extractionPrompt := prompts.BuildExtractionPrompt()
		structured, err := agent.ExtractStructuredReview(ctx, extractionPrompt, result, llm.StructuredConfig{
			ModelOverride: cfg.ExtractionModel,
			MaxTokens:     cfg.MaxTokens,
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

		if err := core.WriteFileWithDirs(cfg.OutputJSONFile, jsonBytes); err != nil {
			return fmt.Errorf("failed to write structured review to %s: %w", cfg.OutputJSONFile, err)
		}
		stderr.Printf("Structured review written to %s\n", cfg.OutputJSONFile)
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
