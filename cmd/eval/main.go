package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/menny/cassandra/core/eval"
	"github.com/menny/cassandra/llm/factory"
)

type config struct {
	SubjectModel    string `mapstructure:"subject-model"`
	SubjectProvider string `mapstructure:"subject-provider"`
	SubjectAPIKey   string `mapstructure:"subject-api-key"`
	SubjectURL      string `mapstructure:"subject-url"`

	JudgeModel      string `mapstructure:"judge-model"`
	JudgeProvider   string `mapstructure:"judge-provider"`
	JudgeAPIKey     string `mapstructure:"judge-api-key"`
	JudgeURL        string `mapstructure:"judge-url"`

	CasesDir        string `mapstructure:"cases-dir"`
	OutputFile      string `mapstructure:"output"`
}

func main() {
	ctx := context.Background()
	stderr := log.New(os.Stderr, "", 0)
	if err := run(ctx, os.Args[1:], stderr); err != nil {
		stderr.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr *log.Logger) error {
	var cfg config

	fs := flag.NewFlagSet("eval", flag.ContinueOnError)

	fs.StringVar(&cfg.SubjectModel, "subject-model", "", "Subject model id")
	fs.StringVar(&cfg.SubjectProvider, "subject-provider", "", "Subject provider")
	fs.StringVar(&cfg.SubjectAPIKey, "subject-api-key", "", "Subject API key")
	fs.StringVar(&cfg.SubjectURL, "subject-url", "", "Subject API URL")

	fs.StringVar(&cfg.JudgeModel, "judge-model", "", "Judge model id")
	fs.StringVar(&cfg.JudgeProvider, "judge-provider", "", "Judge provider")
	fs.StringVar(&cfg.JudgeAPIKey, "judge-api-key", "", "Judge API key")
	fs.StringVar(&cfg.JudgeURL, "judge-url", "", "Judge API URL")

	fs.StringVar(&cfg.CasesDir, "cases-dir", "core/eval/testdata/cases", "Directory containing evaluation cases")
	fs.StringVar(&cfg.OutputFile, "output", "", "Path to write the evaluation results (JSON)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	v := viper.New()
	fs.VisitAll(func(f *flag.Flag) {
		if f.Changed {
			v.BindPFlag(f.Name, f)
		}
	})

	if err := v.Unmarshal(&cfg); err != nil {
		return err
	}

	// Validation
	if cfg.SubjectProvider == "" || cfg.SubjectModel == "" || cfg.SubjectAPIKey == "" {
		return fmt.Errorf("missing subject model configuration")
	}
	if cfg.JudgeProvider == "" || cfg.JudgeModel == "" || cfg.JudgeAPIKey == "" {
		// Default judge to subject if not specified
		if cfg.JudgeProvider == "" { cfg.JudgeProvider = cfg.SubjectProvider }
		if cfg.JudgeModel == "" { cfg.JudgeModel = cfg.SubjectModel }
		if cfg.JudgeAPIKey == "" { cfg.JudgeAPIKey = cfg.SubjectAPIKey }
		if cfg.JudgeURL == "" { cfg.JudgeURL = cfg.SubjectURL }
	}

	subject, err := factory.New(ctx, cfg.SubjectProvider, cfg.SubjectModel, cfg.SubjectAPIKey, cfg.SubjectURL)
	if err != nil {
		return fmt.Errorf("failed to init subject: %w", err)
	}

	judge, err := factory.New(ctx, cfg.JudgeProvider, cfg.JudgeModel, cfg.JudgeAPIKey, cfg.JudgeURL)
	if err != nil {
		return fmt.Errorf("failed to init judge: %w", err)
	}

	cases, err := eval.LoadCases(cfg.CasesDir)
	if err != nil {
		return fmt.Errorf("failed to load cases: %w", err)
	}

	runner := &eval.Runner{
		Subject: subject,
		Judge:   judge,
	}

	var results []eval.CaseResult
	for _, c := range cases {
		stderr.Printf("Running case: %s (%s)...\n", c.Name, c.ID)
		res, err := runner.RunCase(ctx, c)
		if err != nil {
			stderr.Printf("  Error running case %s: %v\n", c.ID, err)
			results = append(results, eval.CaseResult{
				CaseID: c.ID,
				CaseName: c.Name,
				Error: err.Error(),
			})
			continue
		}
		results = append(results, *res)
		stderr.Printf("  Score: %d/5\n", res.Subject.Score)
		stderr.Printf("  Rationale: %s\n", res.Subject.Rationale)
	}

	if cfg.OutputFile != "" {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(cfg.OutputFile, b, 0644); err != nil {
			return err
		}
		stderr.Printf("Results written to %s\n", cfg.OutputFile)
	}

	return nil
}
