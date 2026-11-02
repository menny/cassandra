package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
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
		diff, files, err := FetchGitDiff(context.Background(), tmpDir, "main", "feature", util.DefaultLockFiles)
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

		_, files, err := FetchGitDiff(context.Background(), tmpDir, "main", "feature", util.DefaultLockFiles)
		if err != nil {
			t.Fatalf("FetchGitDiff failed: %v", err)
		}

		for _, f := range files {
			if f == "go.sum" {
				t.Errorf("did NOT expect go.sum in files, got %v", files)
			}
		}
	})

	t.Run("HEAD vs specific branch", func(t *testing.T) {
		// Create uncommitted change (tracked)
		uncommittedPath := filepath.Join(tmpDir, "uncommitted.txt")
		err := os.WriteFile(uncommittedPath, []byte("uncommitted"), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "uncommitted.txt")

		// head="HEAD" should include uncommitted changes
		_, files, err := FetchGitDiff(context.Background(), tmpDir, "main", "HEAD", util.DefaultLockFiles)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range files {
			if f == "uncommitted.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected to find uncommitted.txt when head is HEAD")
		}

		// head="feature" should NOT include uncommitted changes (uses triple-dot)
		_, files, err = FetchGitDiff(context.Background(), tmpDir, "main", "feature", util.DefaultLockFiles)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if f == "uncommitted.txt" {
				t.Error("did NOT expect to find uncommitted.txt when head is NOT HEAD")
			}
		}
	})
}

func TestFetchGitDiff_CustomIgnore(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	// Create a branch so we have a diff
	runGitCmd(t, tmpDir, "checkout", "-b", "custom-branch")

	// Create a file that we want to ignore specifically
	customPath := filepath.Join(tmpDir, "custom.ignore")
	err := os.WriteFile(customPath, []byte("ignore me"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, tmpDir, "add", "custom.ignore")

	// Also add go.sum, which should NOT be ignored since we override the list
	sumPath := filepath.Join(tmpDir, "go.sum")
	err = os.WriteFile(sumPath, []byte("some sum"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, tmpDir, "add", "go.sum")

	runGitCmd(t, tmpDir, "commit", "-m", "add custom ignore and go.sum")

	t.Run("Custom ignore list", func(t *testing.T) {
		ignored := []string{"custom.ignore"}
		_, files, err := FetchGitDiff(context.Background(), tmpDir, "main", "HEAD", ignored)
		if err != nil {
			t.Fatal(err)
		}

		foundGoSum := false
		for _, f := range files {
			if f == "custom.ignore" {
				t.Error("did NOT expect custom.ignore in files")
			}
			if f == "go.sum" {
				foundGoSum = true
			}
		}
		if !foundGoSum {
			t.Error("expected go.sum in files because it was not in the custom ignore list")
		}
	})
}

func TestFetchGitCommits(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	t.Run("Commits between main and feature", func(t *testing.T) {
		commits, err := FetchGitCommits(context.Background(), tmpDir, "main", "feature")
		if err != nil {
			t.Fatalf("FetchGitCommits failed: %v", err)
		}

		if !strings.Contains(commits, "- feature commit") {
			t.Errorf("expected commit message 'feature commit', got:\n%s", commits)
		}
		if strings.Contains(commits, "main new commit") {
			t.Errorf("did NOT expect commit message 'main new commit', got:\n%s", commits)
		}
	})

	t.Run("Commits with HEAD", func(t *testing.T) {
		// Add an uncommitted change and commit it
		runGitCmd(t, tmpDir, "checkout", "feature")
		err := os.WriteFile(filepath.Join(tmpDir, "extra.txt"), []byte("extra"), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "extra.txt")
		runGitCmd(t, tmpDir, "commit", "-m", "extra commit")

		commits, err := FetchGitCommits(context.Background(), tmpDir, "main", "HEAD")
		if err != nil {
			t.Fatalf("FetchGitCommits failed: %v", err)
		}

		if !strings.Contains(commits, "- feature commit") {
			t.Errorf("expected commit message 'feature commit', got:\n%s", commits)
		}
		if !strings.Contains(commits, "- extra commit") {
			t.Errorf("expected commit message 'extra commit', got:\n%s", commits)
		}
	})
}

func TestLocalGetFileDiff(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	t.Run("git diff fallback for changed file", func(t *testing.T) {
		r := NewRegistry()
		RegisterLocalTools(r, tmpDir, "main", "feature", util.DefaultLockFiles, "", false, false, nil, nil)

		args := `{"file_path":"feature.txt"}`
		res, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "get_file_diff",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(res, "+feature content") {
			t.Errorf("expected diff to contain '+feature content', got: %s", res)
		}
	})

	t.Run("in-memory diffMap provider", func(t *testing.T) {
		r := NewRegistry()
		diffMap := map[string]string{
			"custom.go": "diff --git a/custom.go b/custom.go\n+in-memory diff content",
		}
		RegisterLocalTools(r, tmpDir, "main", "HEAD", util.DefaultLockFiles, "", false, false, nil, func() map[string]string {
			return diffMap
		})

		args := `{"file_path":"custom.go"}`
		res, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "get_file_diff",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(res, "+in-memory diff content") {
			t.Errorf("expected in-memory diff content, got: %s", res)
		}
	})

	t.Run("ignored lockfile", func(t *testing.T) {
		r := NewRegistry()
		RegisterLocalTools(r, tmpDir, "main", "HEAD", util.DefaultLockFiles, "", false, false, nil, nil)

		args := `{"file_path":"go.sum"}`
		res, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "get_file_diff",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(res, "Lockfile diffs are ignored by default") {
			t.Errorf("expected lockfile ignored message, got: %s", res)
		}
	})

	t.Run("unchanged file", func(t *testing.T) {
		r := NewRegistry()
		RegisterLocalTools(r, tmpDir, "main", "feature", util.DefaultLockFiles, "", false, false, nil, nil)

		args := `{"file_path":"initial.txt"}`
		res, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "get_file_diff",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(res, "No diff found for initial.txt") {
			t.Errorf("expected no diff found message, got: %s", res)
		}
	})
}

func gitRevParse(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s failed: %v\nOutput: %s", rev, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestResolveBase(t *testing.T) {
	ctx := context.Background()

	t.Run("Local branch resolves to merge-base", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		// main and feature diverged at the initial commit.
		want := gitRevParse(t, tmpDir, "feature~1")
		if got := ResolveBase(ctx, tmpDir, "main", "feature"); got != want {
			t.Errorf("ResolveBase = %q, want merge-base %q", got, want)
		}
	})

	t.Run("Remote-tracking ref wins over stale local branch", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		newMain := gitRevParse(t, tmpDir, "main")
		// Simulate origin/main having advanced while the local main stays stale.
		runGitCmd(t, tmpDir, "update-ref", "refs/remotes/origin/main", newMain)
		// A PR branch cut from the up-to-date main.
		runGitCmd(t, tmpDir, "checkout", "-b", "pr", newMain)
		runGitCmd(t, tmpDir, "branch", "-f", "main", "feature~1")
		if err := os.WriteFile(filepath.Join(tmpDir, "pr.txt"), []byte("pr"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "pr.txt")
		runGitCmd(t, tmpDir, "commit", "-m", "pr commit")

		if got := ResolveBase(ctx, tmpDir, "main", "HEAD"); got != newMain {
			t.Errorf("ResolveBase = %q, want %q (origin/main merge-base, not the stale local main)", got, newMain)
		}

		_, files, err := FetchGitDiff(ctx, tmpDir, ResolveBase(ctx, tmpDir, "main", "HEAD"), "HEAD", util.DefaultLockFiles)
		if err != nil {
			t.Fatalf("FetchGitDiff failed: %v", err)
		}
		if len(files) != 1 || files[0] != "pr.txt" {
			t.Errorf("diff files = %v, want only [pr.txt]", files)
		}
	})

	t.Run("SHA input", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		mainSHA := gitRevParse(t, tmpDir, "main")
		want := gitRevParse(t, tmpDir, "feature~1")
		if got := ResolveBase(ctx, tmpDir, mainSHA, "feature"); got != want {
			t.Errorf("ResolveBase = %q, want %q", got, want)
		}
	})

	t.Run("Unresolvable base is returned unchanged", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepo(t, tmpDir)

		if got := ResolveBase(ctx, tmpDir, "no-such-branch", "feature"); got != "no-such-branch" {
			t.Errorf("ResolveBase = %q, want unchanged", got)
		}
	})

	t.Run("Fetches the base branch from origin first", func(t *testing.T) {
		upstream := t.TempDir()
		setupGitRepo(t, upstream)

		clone := filepath.Join(t.TempDir(), "clone")
		runGitCmd(t, upstream, "clone", "-q", upstream, clone)
		runGitCmd(t, clone, "config", "user.email", "test@example.com")
		runGitCmd(t, clone, "config", "user.name", "Test User")
		runGitCmd(t, clone, "config", "commit.gpgsign", "false")
		runGitCmd(t, clone, "checkout", "-q", "-b", "pr")
		if err := os.WriteFile(filepath.Join(clone, "pr.txt"), []byte("pr"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, clone, "add", "pr.txt")
		runGitCmd(t, clone, "commit", "-q", "-m", "pr commit")
		branchPoint := gitRevParse(t, clone, "origin/main")

		// origin/main advances after the clone; the clone's tracking ref is now stale.
		if err := os.WriteFile(filepath.Join(upstream, "later.txt"), []byte("later"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, upstream, "add", "later.txt")
		runGitCmd(t, upstream, "commit", "-q", "-m", "later main commit")
		newMain := gitRevParse(t, upstream, "main")
		if got := gitRevParse(t, clone, "origin/main"); got != branchPoint {
			t.Fatalf("test setup: origin/main = %q, want stale %q", got, branchPoint)
		}

		// The PR branch still branched from the old tip, so that remains the merge-base,
		// but the remote-tracking ref must have been refreshed along the way.
		if got := ResolveBase(ctx, clone, "main", "HEAD"); got != branchPoint {
			t.Errorf("ResolveBase = %q, want merge-base %q", got, branchPoint)
		}
		if got := gitRevParse(t, clone, "origin/main"); got != newMain {
			t.Errorf("origin/main = %q after ResolveBase, want fetched %q", got, newMain)
		}
	})

	t.Run("Empty base and non-git dir", func(t *testing.T) {
		if got := ResolveBase(ctx, t.TempDir(), "", "HEAD"); got != "" {
			t.Errorf("ResolveBase(empty) = %q, want empty", got)
		}
		if got := ResolveBase(ctx, t.TempDir(), "main", "HEAD"); got != "main" {
			t.Errorf("ResolveBase(non-git) = %q, want unchanged", got)
		}
	})
}
