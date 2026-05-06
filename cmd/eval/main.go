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
		subjectModel    string
		subjectProvider string
		subjectAPIKey   string
		subjectURL      string

		judgeModel      string
		judgeProvider   string
		judgeAPIKey     string
		judgeURL        string

		casesDir   string
		outputFile string
	)

	fs := flag.NewFlagSet("eval", flag.ContinueOnError)

	fs.StringVar(&subjectModel, "subject-model", "", "Subject model id")
	fs.StringVar(&subjectProvider, "subject-provider", "", "Subject provider")
	fs.StringVar(&subjectAPIKey, "subject-api-key", "", "Subject API key")
	fs.StringVar(&subjectURL, "subject-url", "", "Subject API URL")

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

	// Validation
	if subjectProvider == "" || subjectModel == "" || subjectAPIKey == "" {
		return fmt.Errorf("missing required subject flags: --subject-provider, --subject-model, --subject-api-key")
	}

	// Default judge to subject if not specified
	if judgeProvider == "" {
		judgeProvider = subjectProvider
	}
	if judgeModel == "" {
		judgeModel = subjectModel
	}
	if judgeAPIKey == "" {
		judgeAPIKey = subjectAPIKey
	}
	if judgeURL == "" {
		judgeURL = subjectURL
	}

	subject, err := factory.New(ctx, subjectProvider, subjectModel, subjectAPIKey, subjectURL)
	if err != nil {
		return fmt.Errorf("failed to init subject: %w", err)
	}

	judge, err := factory.New(ctx, judgeProvider, judgeModel, judgeAPIKey, judgeURL)
	if err != nil {
		return fmt.Errorf("failed to init judge: %w", err)
	}

	cases, err := eval.LoadCases(casesDir)
	if err != nil {
		return fmt.Errorf("failed to load cases from %s: %w", casesDir, err)
	}

	runner := &eval.Runner{
		Subject: subject,
		Judge:   judge,
	}

	var results []eval.CaseResult
	for _, c := range cases {
		// Respect context cancellation between cases
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
		if err := os.WriteFile(outputFile, b, 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		stderr.Printf("Results written to %s\n", outputFile)
	}

	return nil
}
