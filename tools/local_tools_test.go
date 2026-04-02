package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/menny/cassandra/llm"
)

func TestLocalReadFile(t *testing.T) {
	r := NewRegistry()
	registerLocalReadFile(r)

	// Create a temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "hello AI"
	err := os.WriteFile(testFile, []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"file_path": testFile})
	result, err := r.HandleCall(llm.ToolCall{
		Name:      "read_file",
		Arguments: string(args),
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if result != content {
		t.Errorf("expected %q, got %q", content, result)
	}
}

func TestLocalGlobFiles(t *testing.T) {
	r := NewRegistry()
	registerLocalGlobFiles(r)

	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "file1.go"), []byte(""), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte(""), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"directory": tmpDir, "query": ".go"})
	result, err := r.HandleCall(llm.ToolCall{
		Name:      "glob_files",
		Arguments: string(args),
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if !strings.Contains(result, "file1.go") {
		t.Errorf("expected result to contain file1.go, got: %s", result)
	}
	if strings.Contains(result, "file2.txt") {
		t.Errorf("expected result not to contain file2.txt, got: %s", result)
	}
}

func TestLocalReadFile_Errors(t *testing.T) {
	r := NewRegistry()
	registerLocalReadFile(r)

	t.Run("missing file", func(t *testing.T) {
		_, err := r.HandleCall(llm.ToolCall{
			Name:      "read_file",
			Arguments: `{"file_path":"non_existent_file.txt"}`,
		})
		if err == nil {
			t.Error("expected error for non-existent file, got nil")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		_, err := r.HandleCall(llm.ToolCall{
			Name:      "read_file",
			Arguments: `{"file_path": 123}`, // Should be string
		})
		if err == nil {
			t.Error("expected error for malformed JSON types, got nil")
		}
	})
}

func TestLocalGlobFiles_Errors(t *testing.T) {
	r := NewRegistry()
	registerLocalGlobFiles(r)

	t.Run("invalid directory", func(t *testing.T) {
		// filepath.WalkDir doesn't necessarily error if the root doesn't exist depending on OS,
		// but we can test malformed JSON.
		_, err := r.HandleCall(llm.ToolCall{
			Name:      "glob_files",
			Arguments: `{"query": 123}`,
		})
		if err == nil {
			t.Error("expected error for malformed JSON query, got nil")
		}
	})
}

func TestLocalGrepFiles(t *testing.T) {
	r := NewRegistry()
	registerLocalGrepFiles(r)

	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	// Create a file with some content
	testFile := filepath.Join(tmpDir, "grep_test.txt")
	err := os.WriteFile(testFile, []byte("Hello Cassandra\nThis is a grep test.\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, tmpDir, "add", "grep_test.txt")

	// We need to change the working directory for git grep to work in the temp repo
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	t.Run("basic grep", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "Cassandra"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if !strings.Contains(result, "grep_test.txt:1:7:Hello Cassandra") {
			t.Errorf("expected result to contain match, got: %s", result)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "NonExistentString"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success (no error for no matches), got error: %v", err)
		}
		if result != "No matches found." {
			t.Errorf("expected 'No matches found.', got: %s", result)
		}
	})

	t.Run("grep in directory", func(t *testing.T) {
		subdir := filepath.Join(tmpDir, "subdir")
		if err := os.Mkdir(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		subFile := filepath.Join(subdir, "subfile.txt")
		if err := os.WriteFile(subFile, []byte("Match in subdir"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "subdir/subfile.txt")

		args, _ := json.Marshal(map[string]any{"query": "Match", "directory": "subdir"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if !strings.Contains(result, "subdir/subfile.txt:1:1:Match in subdir") {
			t.Errorf("expected result to contain match in subdir, got: %s", result)
		}
	})

	t.Run("unstaged changes", func(t *testing.T) {
		// Modify the tracked file but don't add it
		err := os.WriteFile(testFile, []byte("Hello Cassandra Modified\nThis is a grep test.\n"), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"query": "Modified"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "grep_test.txt:1:17:Hello Cassandra Modified") {
			t.Errorf("expected result to contain unstaged change, got: %s", result)
		}
	})

	t.Run("untracked files", func(t *testing.T) {
		untrackedFile := filepath.Join(tmpDir, "untracked.txt")
		err := os.WriteFile(untrackedFile, []byte("Untracked match"), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"query": "Untracked"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "untracked.txt:1:1:Untracked match") {
			t.Errorf("expected result to contain untracked file match, got: %s", result)
		}
	})

	t.Run("query with hyphen", func(t *testing.T) {
		// Use a pattern that looks like a flag
		args, _ := json.Marshal(map[string]any{"query": "-Cassandra"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if result != "No matches found." {
			t.Errorf("expected 'No matches found.', got: %s", result)
		}
	})

	t.Run("truncation limit", func(t *testing.T) {
		truncFile := filepath.Join(tmpDir, "truncation.txt")
		var sb strings.Builder
		for i := 0; i < 110; i++ {
			sb.WriteString(fmt.Sprintf("TruncMatch line %d\n", i))
		}
		if err := os.WriteFile(truncFile, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "truncation.txt")

		args, _ := json.Marshal(map[string]any{"query": "TruncMatch"})
		result, err := r.HandleCall(llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(strings.TrimSpace(result), "\n")
		// 100 lines of matches + 1 line for "truncated" message
		if len(lines) != 101 {
			t.Errorf("expected 101 lines, got %d", len(lines))
		}
		if !strings.Contains(result, "... (truncated, 10 more matches)") {
			t.Errorf("expected truncation message, got: %s", result)
		}
	})
}
