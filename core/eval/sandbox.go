package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Sandbox provides a high-fidelity git environment for evaluating the agent.
type Sandbox struct {
	RootDir string
}

// NewSandbox initializes a new git-backed sandbox.
// It copies files from baseSourceDir (if non-empty), initializes git,
// commits the base state, applies the diff, and commits the final state.
func NewSandbox(ctx context.Context, baseSourceDir string, diff string) (*Sandbox, error) {
	tmp, err := os.MkdirTemp("", "cassandra-eval-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	s := &Sandbox{RootDir: tmp}

	// 1. Copy base files
	if baseSourceDir != "" {
		if err := copyDir(baseSourceDir, tmp); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to copy base files: %w", err)
		}
	}

	// 2. Git init
	if err := s.runGit(ctx, "init", "--initial-branch=main"); err != nil {
		s.Cleanup()
		return nil, err
	}
	// Configure git identity for the sandbox
	if err := s.runGit(ctx, "config", "user.email", "eval@cassandra.ai"); err != nil {
		s.Cleanup()
		return nil, err
	}
	if err := s.runGit(ctx, "config", "user.name", "Cassandra Eval"); err != nil {
		s.Cleanup()
		return nil, err
	}

	// 3. Commit base state
	// We need to check if there are any files to add
	files, _ := os.ReadDir(tmp)
	if len(files) > 0 {
		if err := s.runGit(ctx, "add", "."); err != nil {
			s.Cleanup()
			return nil, err
		}
		if err := s.runGit(ctx, "commit", "-m", "base state"); err != nil {
			s.Cleanup()
			return nil, err
		}
	} else {
		// Even if empty, create an initial commit to avoid "no branch" errors
		if err := s.runGit(ctx, "commit", "--allow-empty", "-m", "initial empty commit"); err != nil {
			s.Cleanup()
			return nil, err
		}
	}

	// 4. Apply diff
	if diff != "" {
		diffFile := filepath.Join(tmp, "input.diff")
		if err := os.WriteFile(diffFile, []byte(diff), 0644); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to write diff file: %w", err)
		}
		// Use git apply for higher fidelity
		if err := s.runGit(ctx, "apply", "input.diff"); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to apply diff: %w", err)
		}
		if err := os.Remove(diffFile); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to remove diff file: %w", err)
		}

		// 5. Commit diff state
		if err := s.runGit(ctx, "add", "."); err != nil {
			s.Cleanup()
			return nil, err
		}
		if err := s.runGit(ctx, "commit", "-m", "applied diff"); err != nil {
			s.Cleanup()
			return nil, err
		}
	}

	return s, nil
}

// Cleanup removes the sandbox directory.
func (s *Sandbox) Cleanup() {
	if s.RootDir != "" {
		os.RemoveAll(s.RootDir)
	}
}

func (s *Sandbox) runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.RootDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed in %s: %w\nOutput: %s", args, s.RootDir, err, string(out))
	}
	return nil
}

func copyDir(src string, dst string) error {
	return os.CopyFS(dst, os.DirFS(src))
}
