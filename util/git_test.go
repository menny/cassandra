package util

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitCmd(t *testing.T, dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, string(out))
	}
}

func TestRunGitWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	runGitCmd(t, tmpDir, "init", "-b", "main")
	runGitCmd(t, tmpDir, "config", "user.email", "test@example.com")
	runGitCmd(t, tmpDir, "config", "user.name", "Test User")
	runGitCmd(t, tmpDir, "config", "commit.gpgsign", "false")

	// Create a file with multiple lines
	testFile := filepath.Join(tmpDir, "large.txt")
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("some random data line\n")
	}
	if err := os.WriteFile(testFile, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, tmpDir, "add", "large.txt")

	t.Run("no truncation", func(t *testing.T) {
		out, truncated, err := RunGitWithLimit(context.Background(), tmpDir, 100000, "grep", "some random data", "large.txt")
		if err != nil {
			t.Fatalf("expected success, got err: %v", err)
		}
		if truncated {
			t.Error("expected truncated=false, got true")
		}
		if len(out) == 0 {
			t.Error("expected output, got empty")
		}
	})

	t.Run("with truncation", func(t *testing.T) {
		out, truncated, err := RunGitWithLimit(context.Background(), tmpDir, 100, "grep", "some random data", "large.txt")
		if err != nil {
			t.Fatalf("expected success, got err: %v", err)
		}
		if !truncated {
			t.Error("expected truncated=true, got false")
		}
		if len(out) != 100 {
			t.Errorf("expected output length to be exactly 100, got %d", len(out))
		}
	})

	t.Run("ceiling limit exceeded", func(t *testing.T) {
		_, _, err := RunGitWithLimit(context.Background(), tmpDir, MaxGitLimitBytes+1, "grep", "some random data", "large.txt")
		if err == nil {
			t.Error("expected error for exceeding MaxGitLimitBytes, got nil")
		}
	})

	t.Run("negative limit", func(t *testing.T) {
		_, _, err := RunGitWithLimit(context.Background(), tmpDir, -1, "grep", "some random data", "large.txt")
		if err == nil {
			t.Error("expected error for negative limit, got nil")
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, _, err := RunGitWithLimit(ctx, tmpDir, 1000, "grep", "some random data", "large.txt")
		if err == nil {
			t.Error("expected error for cancelled context, got nil")
		}
	})
}
