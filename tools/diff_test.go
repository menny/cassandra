package tools

import (
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

func setupGitRepo(t *testing.T, dir string) {
	runGitCmd(t, dir, "init", "-b", "main")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")
	runGitCmd(t, dir, "config", "commit.gpgsign", "false")

	// Initial commit on main
	err := os.WriteFile(filepath.Join(dir, "initial.txt"), []byte("initial"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "initial.txt")
	runGitCmd(t, dir, "commit", "-m", "initial commit")

	// Create a branch and add a file
	runGitCmd(t, dir, "checkout", "-b", "feature")
	err = os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature content"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "feature.txt")
	runGitCmd(t, dir, "commit", "-m", "feature commit")

	// Go back to main and add a file (diverge)
	runGitCmd(t, dir, "checkout", "main")
	err = os.WriteFile(filepath.Join(dir, "main_new.txt"), []byte("main new"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", "main_new.txt")
	runGitCmd(t, dir, "commit", "-m", "main new commit")
}

func TestFetchGitDiff(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	t.Run("Triple-dot diff", func(t *testing.T) {
		// diff main...feature should ONLY show feature.txt
		// NOT main_new.txt
		diff, files, err := FetchGitDiff(tmpDir, "main", "feature")
		if err != nil {
			t.Fatalf("FetchGitDiff failed: %v", err)
		}

		foundFeature := false
		foundMainNew := false
		for _, f := range files {
			if f == "feature.txt" {
				foundFeature = true
			}
			if f == "main_new.txt" {
				foundMainNew = true
			}
		}

		if !foundFeature {
			t.Errorf("expected feature.txt in files, got %v", files)
		}
		if foundMainNew {
			t.Errorf("did NOT expect main_new.txt in files, got %v", files)
		}

		if !strings.Contains(diff, "feature content") {
			t.Errorf("expected diff to contain feature content, got:\n%s", diff)
		}
		if strings.Contains(diff, "main new") {
			t.Errorf("did NOT expect diff to contain main new, got:\n%s", diff)
		}
	})

	t.Run("Exclusion rules", func(t *testing.T) {
		// Create a branch with a lock file
		runGitCmd(t, tmpDir, "checkout", "feature")
		err := os.WriteFile(filepath.Join(tmpDir, "go.sum"), []byte("dummy sum"), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "go.sum")
		runGitCmd(t, tmpDir, "commit", "-m", "add go.sum")

		_, files, err := FetchGitDiff(tmpDir, "main", "feature")
		if err != nil {
			t.Fatalf("FetchGitDiff failed: %v", err)
		}

		for _, f := range files {
			if f == "go.sum" {
				t.Errorf("did NOT expect go.sum in files, got %v", files)
			}
		}
	})
}
