package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/eval"
	"github.com/menny/cassandra/llm/factory"
)

func main() {
	// Handle termination signals gracefully
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	stderr := log.New(os.Stderr, "", 0)
	if err := run(ctx, os.Args[1:], stderr); err != nil {
		stderr.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr *log.Logger) error {
	var (
		subjectConfigPath string
		subjectAPIKey     string

		judgeModel    string
		judgeProvider string
		judgeAPIKey   string
		judgeURL      string

		casesDir   string
		outputFile string
	)

	fs := flag.NewFlagSet("eval", flag.ContinueOnError)

	fs.StringVar(&subjectConfigPath, "subject-config", "", "Path to the subject's cassandra.toml")
	fs.StringVar(&subjectAPIKey, "subject-api-key", "", "API key for the subject (overrides config if provided)")

	fs.StringVar(&judgeModel, "judge-model", "", "Judge model id")
	fs.StringVar(&judgeProvider, "judge-provider", "", "Judge provider")
	fs.StringVar(&judgeAPIKey, "judge-api-key", "", "Judge API key")
	fs.StringVar(&judgeURL, "judge-url", "", "Judge API URL")

	fs.StringVar(&casesDir, "cases-dir", "core/eval/testdata/cases", "Directory containing evaluation cases")
	fs.StringVar(&outputFile, "output", "", "Path to write the evaluation results (JSON)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// 1. Load Subject Config
	subjectCfg, err := config.Load(subjectConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load subject config: %w", err)
	}
	if subjectAPIKey != "" {
		subjectCfg.ProviderAPIKey = subjectAPIKey
	}

	// Validation
	if subjectCfg.Provider == "" || subjectCfg.Model == "" || subjectCfg.ProviderAPIKey == "" {
		return fmt.Errorf("subject configuration is incomplete (provider, model, and api-key are required)")
	}

	// 2. Setup Judge
	// Default judge to subject if not specified
	if judgeProvider == "" {
		judgeProvider = subjectCfg.Provider
	}
	if judgeModel == "" {
		judgeModel = subjectCfg.Model
	}
	if judgeAPIKey == "" {
		judgeAPIKey = subjectCfg.ProviderAPIKey
	}
	if judgeURL == "" {
		judgeURL = subjectCfg.ProviderURL
	}

	judge, err := factory.New(ctx, judgeProvider, judgeModel, judgeAPIKey, judgeURL)
	if err != nil {
		return fmt.Errorf("failed to init judge: %w", err)
	}

	// 3. Run Evaluations
	cases, err := eval.LoadCases(casesDir)
	if err != nil {
		return fmt.Errorf("failed to load cases from %s: %w", casesDir, err)
	}

	runner := &eval.Runner{
		SubjectConfig: subjectCfg,
		Judge:         judge,
	}

	var results []eval.CaseResult
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return err
		}

		stderr.Printf("Running case: %s (%s)...\n", c.Name, c.ID)
		res, err := runner.RunCase(ctx, c)
		if err != nil {
			stderr.Printf("  Error running case %s: %v\n", c.ID, err)
			results = append(results, eval.CaseResult{
				CaseID:   c.ID,
				CaseName: c.Name,
				Error:    err.Error(),
			})
			continue
		}
		results = append(results, *res)
		stderr.Printf("  Score: %d/5\n", res.Subject.Score)
		stderr.Printf("  Rationale: %s\n", res.Subject.Rationale)
		if len(res.Subject.Findings) > 0 {
			stderr.Printf("  Findings:\n")
			for _, f := range res.Subject.Findings {
				stderr.Printf("    - %s\n", f)
			}
		}
	}

	if outputFile != "" {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal results: %w", err)
		}
		if err := os.WriteFile(outputFile, b, 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		stderr.Printf("Results written to %s\n", outputFile)
	}

	return nil
}
