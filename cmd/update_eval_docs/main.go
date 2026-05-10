package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/menny/cassandra/core/eval"
)

func main() {
	var (
		resultsPath     string
		configPath      string
		suitePath       string
		id              string
		evaluationsFile string
	)

	flag.StringVar(&resultsPath, "results", "", "Path to the JSON results file")
	flag.StringVar(&configPath, "config", "cassandra.toml", "Path to the subject's cassandra.toml")
	flag.StringVar(&suitePath, "suite", "core/eval/testdata/evaluations.json", "Path to the evaluation suite manifest (JSON)")
	flag.StringVar(&id, "id", "", "The ID of the injection site in EVALUATIONS.md")
	flag.StringVar(&evaluationsFile, "md", "core/eval/EVALUATIONS.md", "Path to the EVALUATIONS.md file")
	flag.Parse()

	// Move to the intended working directory if executing via bazel
	if workspaceDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); workspaceDir != "" {
		if err := os.Chdir(workspaceDir); err != nil {
			log.Fatalf("failed to change directory to %s: %v", workspaceDir, err)
		}
	}

	if resultsPath == "" {
		log.Fatal("--results is required")
	}
	if id == "" {
		log.Fatal("--id is required")
	}

	// 1. Load results
	resultsData, err := os.ReadFile(resultsPath)
	if err != nil {
		log.Fatalf("failed to read results: %v", err)
	}

	var results []eval.CaseResult
	if err := json.Unmarshal(resultsData, &results); err != nil {
		log.Fatalf("failed to parse results: %v", err)
	}

	// 2. Load suite to get rubrics
	suite, err := eval.LoadSuite(suitePath)
	if err != nil {
		log.Fatalf("failed to load suite: %v", err)
	}

	rubrics := make(map[string]string)
	for _, c := range suite.Cases {
		rubrics[c.ID] = c.Rubric
	}

	// 3. Generate Markdown
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Config**: `%s`  \n\n", configPath))

	sb.WriteString("| Eval ID | Eval Name | Judge Criteria | Score |\n")
	sb.WriteString("| --- | --- | --- | --- |\n")

	for _, res := range results {
		rubric := rubrics[res.CaseID]
		// Clean up rubric for table (no newlines, escape pipes)
		rubric = strings.ReplaceAll(rubric, "\n", " ")
		rubric = strings.ReplaceAll(rubric, "|", "\\|")

		scoreStr := fmt.Sprintf("%d/5", res.Subject.Score)
		if res.Error != "" {
			scoreStr = "ERROR"
		}

		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", res.CaseID, res.CaseName, rubric, scoreStr))
	}
	sb.WriteString("\n")

	newContent := sb.String()

	// 4. Update EVALUATIONS.md
	mdContent, err := os.ReadFile(evaluationsFile)
	if err != nil {
		log.Fatalf("failed to read evaluations file: %v", err)
	}

	startMarker := fmt.Sprintf("<!-- EVAL_RESULTS_START:%s -->", id)
	endMarker := fmt.Sprintf("<!-- EVAL_RESULTS_END:%s -->", id)

	content := string(mdContent)
	startIndex := strings.Index(content, startMarker)
	endIndex := strings.Index(content, endMarker)

	if startIndex == -1 || endIndex == -1 {
		log.Fatalf("could not find markers for ID %s in %s. Please ensure both <!-- EVAL_RESULTS_START:%s --> and <!-- EVAL_RESULTS_END:%s --> are present.", id, evaluationsFile, id, id)
	}
	if startIndex >= endIndex {
		log.Fatalf("invalid marker positions for ID %s in %s: start marker must appear before end marker", id, evaluationsFile)
	}

	finalContent := content[:startIndex+len(startMarker)] + "\n" + newContent + content[endIndex:]

	if err := os.WriteFile(evaluationsFile, []byte(finalContent), 0o644); err != nil {
		log.Fatalf("failed to write evaluations file: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully updated %s section '%s' with results from %s\n", evaluationsFile, id, resultsPath)
}
