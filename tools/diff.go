package tools

import (
	"fmt"
	"os/exec"

	"github.com/tmc/langchaingo/llms"
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

func registerLocalGitDiff(r *Registry, diffBranch string) {
	def := llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "get_git_diff",
			Description: "Gets the git diff of the current repository, automatically excluding lockfiles. Useful for reviewing what changes the user has made.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}

	r.RegisterTool(def, func(args map[string]any) (string, error) {
		cmdArgs := []string{"diff"}
		if diffBranch != "" {
			cmdArgs = append(cmdArgs, diffBranch)
		}

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
	})
}
