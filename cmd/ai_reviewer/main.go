package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
	"github.com/menny/cassandra/util"
)

func main() {
	ctx := context.Background()
	stderr := log.New(os.Stderr, "", 0)
	if err := run(ctx, os.Args[1:], stderr); err != nil {
		stderr.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr *log.Logger) error {
	cfg := config.NewDefaultConfig()

	fs := flag.NewFlagSet("cassandra", flag.ContinueOnError)

	fs.StringVar(&cfg.WorkingDir, "cwd", "", "Working directory (defaults to BUILD_WORKSPACE_DIRECTORY or current directory)")
	fs.StringVar(&cfg.MainGuidelines, "main-guidelines", "", "Path to a file or a named prompt from the library (defaults to 'general')")
	fs.StringArrayVar(&cfg.SupplementalGuidelines, "supplemental-guidelines", nil, "Optional additive paths or named library prompts for supplemental guidelines (can be used multiple times)")
	fs.StringVar(&cfg.ApprovalEvaluationPromptFile, "approval-evaluation-prompt-file", "", "Optional path to a file containing custom approval evaluation guidelines")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", llm.DefaultMaxTokens, "Max tokens for the LLM response (defaults to provider specific default)")
	fs.StringVar(&cfg.Base, "base", "main", "Base commit/branch for diff (defaults to 'main')")
	fs.StringVar(&cfg.Head, "head", "HEAD", "Head commit/branch for diff (defaults to 'HEAD')")
	fs.StringVar(&cfg.ReviewOutputFile, "review-output-file", "", "Path to a file where the final review will be written")
	fs.StringVar(&cfg.OutputJSONFile, "output-json", "", "Path to a file where the structured JSON review will be written")
	fs.StringVar(&cfg.MetricsJSONFile, "metrics-json", "", "Path to a file where the session metrics will be written")
	fs.StringVar(&cfg.ExtractionModel, "extraction-model", "", "Optional model override for the structured JSON extraction pass (requires --output-json)")
	fs.StringVar(&cfg.MetadataJSONFile, "metadata-json", "", "Path to a JSON file containing PR metadata")
	fs.StringVar(&cfg.DiffFile, "diff-file", "", "Path to a file containing the git diff")
	fs.StringVar(&cfg.FilesListFile, "files-list-file", "", "Path to a file containing the list of changed files (one per line)")
	fs.StringVar(&cfg.CommitsFile, "commits-file", "", "Path to a file containing the commit messages")
	fs.StringVar(&cfg.MCPConfigFile, "mcp-config", "", "Path to an mcp.json file configuring custom tools for the reviewer")
	fs.BoolVar(&cfg.AllowURLFetch, "allow-url-fetch", false, "Enable the mcp-server-fetch tool (requires uvx to be installed)")
	fs.StringSliceVar(&cfg.IgnoredLockFiles, "ignored-lock-files", util.DefaultLockFiles, "Comma-separated list of lock files to ignore in diffs (overrides default)")
	fs.StringVar(&cfg.ConfigFile, "config", "", "Path to a configuration file (toml)")
	fs.StringVar(&cfg.WishlistDir, "wishlist-dir", "", "Path to a directory where AI-Reviewer feedback/wishlist will be stored")

	fs.StringVar(&cfg.Model, "model", "", "LLM provider's model id (e.g. gemini-3-flash-preview, claude-3-7-sonnet-20250219)")
	fs.StringVar(&cfg.Provider, "provider", "", "LLM provider to use (google, anthropic, openai)")
	fs.StringVar(&cfg.ProviderAPIKey, "provider-api-key", "", "API key for the selected provider (required)")
	fs.StringVar(&cfg.ProviderURL, "provider-url", "", "Optional API endpoint URL override (e.g. for OpenAI-compatible providers like Ollama)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if len(fs.Args()) > 0 {
		return fmt.Errorf("unknown or positional arguments provided: %v", fs.Args())
	}

	targetDir := cfg.WorkingDir
	if targetDir == "" {
		// If executing via 'bazel run', BUILD_WORKSPACE_DIRECTORY points to the
		// source root. Otherwise, we default to the current directory.
		targetDir = os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	}
	if targetDir != "" {
		if err := os.Chdir(targetDir); err != nil {
			return fmt.Errorf("failed to change directory to %s: %w", targetDir, err)
		}
	}

	v := viper.New()
	v.SetDefault("main-guidelines", "general")
	v.SetDefault("base", "main")
	v.SetDefault("head", "HEAD")
	v.SetDefault("max-tokens", llm.DefaultMaxTokens)
	v.SetDefault("ignored-lock-files", util.DefaultLockFiles)
	v.SetDefault("allow-url-fetch", false)

	fs.VisitAll(func(f *flag.Flag) {
		if f.Changed {
			if err := v.BindPFlag(f.Name, f); err != nil {
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
		if cfg.ConfigFile != "" {
			return fmt.Errorf("failed to read config file %q: %w", cfg.ConfigFile, err)
		}
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	}

	if err := v.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	trimmed := make([]string, len(cfg.IgnoredLockFiles))
	for i, lf := range cfg.IgnoredLockFiles {
		trimmed[i] = strings.TrimSpace(lf)
	}
	cfg.IgnoredLockFiles = trimmed

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
	if cfg.WishlistDir != "" {
		stderr.Printf("  Wishlist Directory: %s\n", cfg.WishlistDir)
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
	if cfg.MetricsJSONFile != "" {
		stderr.Printf("  Session Metrics JSON: %s\n", cfg.MetricsJSONFile)
	}
	if cfg.MetadataJSONFile != "" {
		stderr.Printf("  Metadata JSON: %s\n", cfg.MetadataJSONFile)
	}
	if cfg.ApprovalEvaluationPromptFile != "" {
		stderr.Printf("  Approval Evaluation Prompt File: %s\n", cfg.ApprovalEvaluationPromptFile)
	}
	stderr.Println("  API Key: [PROVIDED]")
	stderr.Println("===============================")

	reporter := core.NewDefaultReporter(stderr.Writer())
	reviewer, err := core.NewReviewer(ctx, cfg, targetDir, reporter)
	if err != nil {
		return fmt.Errorf("failed to initialize reviewer: %w", err)
	}
	defer reviewer.Close()

	if cfg.MetricsJSONFile != "" {
		defer func() {
			metrics := reviewer.Agent.GetMetrics()
			jsonBytes, err := json.MarshalIndent(map[string]any{"metrics": metrics}, "", "  ")
			if err != nil {
				stderr.Printf("⚠️  Failed to marshal metrics: %v\n", err)
				return
			}

			if err := util.WriteFileWithDirs(cfg.MetricsJSONFile, jsonBytes); err != nil {
				stderr.Printf("⚠️  Failed to write metrics to %s: %v\n", cfg.MetricsJSONFile, err)
				return
			}
			stderr.Printf("📈 Metrics written to %s\n", cfg.MetricsJSONFile)
		}()
	}

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
		stderr.Println("🌿 Fetching git diff...")
		var err error
		diffOutput, changedFiles, err = tools.FetchGitDiff(ctx, targetDir, cfg.Base, cfg.Head, cfg.IgnoredLockFiles)
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
		stderr.Println("🌿 Fetching git commits...")
		commits, err := tools.FetchGitCommits(ctx, targetDir, cfg.Base, cfg.Head)
		if err != nil {
			stderr.Printf("⚠️  Failed to fetch git commits: %v\n", err)
		} else {
			commitsOutput = commits
		}
	}

	if len(changedFiles) == 0 {
		stderr.Println("⚪ No changes found.")
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
			stderr.Printf("⚠️  Failed to read metadata JSON: %v. Proceeding without metadata.\n", err)
		} else {
			var metadata core.PRMetadata
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				stderr.Printf("⚠️  Failed to parse metadata JSON: %v. Proceeding without metadata.\n", err)
			} else {
				requestText = formatMetadata(metadata) + "\n\n" + requestText
			}
		}
	}

	result, err := reviewer.Run(ctx, changedFiles, requestText)
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	reviewer.Agent.Reporter().ReportReviewHeader(len(changedFiles), cfg.MainGuidelines, cfg.Model)

	fmt.Println(result)

	if cfg.ReviewOutputFile != "" {
		if err := util.WriteFileWithDirs(cfg.ReviewOutputFile, []byte(result)); err != nil {
			return fmt.Errorf("failed to write review to %s: %w", cfg.ReviewOutputFile, err)
		}
		stderr.Printf("📝 Review written to %s\n", cfg.ReviewOutputFile)
	}

	if cfg.OutputJSONFile != "" {
		extractionPrompt := prompts.BuildExtractionPrompt()
		structured, err := reviewer.Agent.ExtractStructuredReview(ctx, extractionPrompt, result, llm.StructuredConfig{
			ModelOverride: cfg.ExtractionModel,
			MaxTokens:     cfg.MaxTokens,
		})
		if err != nil {
			return fmt.Errorf("structured extraction failed: %w", err)
		}

		structured.RawFreeText = result

		jsonBytes, err := json.MarshalIndent(structured, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal structured review: %w", err)
		}

		if err := util.WriteFileWithDirs(cfg.OutputJSONFile, jsonBytes); err != nil {
			return fmt.Errorf("failed to write structured review to %s: %w", cfg.OutputJSONFile, err)
		}
		stderr.Printf("📦 Structured review written to %s\n", cfg.OutputJSONFile)
	}

	return nil
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
			lines := strings.Split(c.Body, "\n")
			for _, line := range lines {
				fmt.Fprintf(&sb, "  > %s\n", line)
			}
		}
	}

	return sb.String()
}
