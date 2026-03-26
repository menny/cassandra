package tools

import (
	"fmt"
	"os/exec"
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

func FetchGitDiff(diffBranch string) (string, error) {
	cmdArgs := []string{"diff"}
	cmdArgs = append(cmdArgs, diffBranch)

	cmdArgs = append(cmdArgs, "--", ".")
	for _, lf := range lockFiles {
		// standard git pathspec to exclude files
		cmdArgs = append(cmdArgs, fmt.Sprintf(":(exclude)*%s", lf))
	}

	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %v\nOutput: %s", err, string(out))
	}

	result := string(out)
	if result == "" {
		return "No diff found. The repository is perfectly clean.", nil
	}
	return result, nil
}
