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
		resultsPaths    []string
		configPath      string
		suitePath       string
		id              string
		sha             string
		repo            string
		evaluationsFile string
	)

	flag.Func("results", "Path to the JSON results file (can be specified multiple times)", func(s string) error {
		resultsPaths = append(resultsPaths, s)
		return nil
	})
	flag.StringVar(&configPath, "config", "cassandra.toml", "Path to the subject's cassandra.toml")
	flag.StringVar(&suitePath, "suite", "core/eval/testdata/evaluations.json", "Path to the evaluation suite manifest (JSON)")
	flag.StringVar(&id, "id", "", "The ID of the injection site in EVALUATIONS.md")
	flag.StringVar(&sha, "sha", "", "The commit SHA being evaluated")
	flag.StringVar(&repo, "repo", "menny/cassandra", "The GitHub repository (owner/repo)")
	flag.StringVar(&evaluationsFile, "md", "core/eval/EVALUATIONS.md", "Path to the EVALUATIONS.md file")
	flag.Parse()

	// Move to the intended working directory if executing via bazel
	if workspaceDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); workspaceDir != "" {
		if err := os.Chdir(workspaceDir); err != nil {
			log.Fatalf("failed to change directory to %s: %v", workspaceDir, err)
		}
	}

	if len(resultsPaths) == 0 {
		log.Fatal("--results is required (at least once)")
	}
	if id == "" {
		log.Fatal("--id is required")
	}

	// 1. Load all results
	allResults := make(map[string][]eval.CaseResult) // map[caseID][]results
	for _, path := range resultsPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("failed to read results from %s: %v", path, err)
		}
		var results []eval.CaseResult
		if err := json.Unmarshal(data, &results); err != nil {
			log.Fatalf("failed to parse results from %s: %v", path, err)
		}
		for _, res := range results {
			allResults[res.CaseID] = append(allResults[res.CaseID], res)
		}
	}

	// 2. Load suite to get rubrics and order
	suite, err := eval.LoadSuite(suitePath)
	if err != nil {
		log.Fatalf("failed to load suite: %v", err)
	}

	// 3. Generate Markdown
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Config**: `%s`  \n", configPath))
	if sha != "" {
		displaySha := sha
		if len(sha) > 7 {
			displaySha = sha[:7]
		}
		sb.WriteString(fmt.Sprintf("**Commit**: [`%s`](https://github.com/%s/commit/%s)  \n", displaySha, repo, sha))
	}
	sb.WriteString(fmt.Sprintf("**Runs**: %d  \n\n", len(resultsPaths)))

	sb.WriteString("| Eval ID | Eval Name | Judge Criteria | Min | Max | Mean |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	var totalMin, totalMax, totalMean float64
	caseCount := 0

	for _, c := range suite.Cases {
		results, ok := allResults[c.ID]
		if !ok || len(results) == 0 {
			continue
		}

		var minScore, maxScore int
		var sum int
		count := 0
		hasError := false

		for _, res := range results {
			if res.Error != "" {
				hasError = true
				continue
			}
			score := res.Subject.Score
			if count == 0 || score < minScore {
				minScore = score
			}
			if count == 0 || score > maxScore {
				maxScore = score
			}
			sum += score
			count++
		}

		rubric := strings.ReplaceAll(c.Rubric, "\n", " ")
		rubric = strings.ReplaceAll(rubric, "|", "\\|")

		if hasError && count == 0 {
			sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | ERROR | ERROR | ERROR |\n", c.ID, c.Name, rubric))
		} else {
			mean := float64(sum) / float64(count)
			sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %d | %d | %.2f |\n", c.ID, c.Name, rubric, minScore, maxScore, mean))

			totalMin += float64(minScore)
			totalMax += float64(maxScore)
			totalMean += mean
			caseCount++
		}
	}

	if caseCount > 0 {
		avgMin := totalMin / float64(caseCount)
		avgMax := totalMax / float64(caseCount)
		avgMean := totalMean / float64(caseCount)
		sb.WriteString(fmt.Sprintf("| **OVERALL** | | | **%.2f** | **%.2f** | **%.2f** |\n", avgMin, avgMax, avgMean))
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

	fmt.Fprintf(os.Stderr, "Successfully updated %s section '%s' with results from %d runs\n", evaluationsFile, id, len(resultsPaths))
}
