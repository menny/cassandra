package tools

import (
	"fmt"
	"os/exec"
	"strings"
)

var lockFiles = []string{
	"go.sum",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"Cargo.lock",
	"poetry.lock",
	"Gemfile.lock",
}

func FetchGitDiff(workingDir, base, head string) (string, []string, error) {
	diffRange := fmt.Sprintf("%s...%s", base, head)
	cmdArgs := []string{"diff", diffRange}

	cmdArgs = append(cmdArgs, "--", ".")
	for _, lf := range lockFiles {
		cmdArgs = append(cmdArgs, fmt.Sprintf(":(exclude)*%s", lf))
	}

	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = workingDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("git diff %s failed in %s: %v\nOutput: %s", diffRange, workingDir, err, string(out))
	}

	diffText := string(out)
	if diffText == "" {
		return "No diff found. The repository is perfectly clean.", nil, nil
	}

	// Get file list
	nameOnlyArgs := []string{"diff", "--name-only", diffRange, "--", "."}
	for _, lf := range lockFiles {
		nameOnlyArgs = append(nameOnlyArgs, fmt.Sprintf(":(exclude)*%s", lf))
	}
	nameOnlyCmd := exec.Command("git", nameOnlyArgs...)
	nameOnlyCmd.Dir = workingDir
	nameOnlyOut, err := nameOnlyCmd.CombinedOutput()
	if err != nil {
		return diffText, nil, nil // Fallback if name-only fails
	}
	files := strings.Split(strings.TrimSpace(string(nameOnlyOut)), "\n")
	var filteredFiles []string
	for _, f := range files {
		if f != "" {
			filteredFiles = append(filteredFiles, f)
		}
	}

	return diffText, filteredFiles, nil
}
