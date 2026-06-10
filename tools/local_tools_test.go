package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

func TestLocalReadFile(t *testing.T) {
	r := NewRegistry()
	registerLocalReadFile(r, "")

	// Create a temp file with multiple lines
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	contentLines := []string{"line 1", "line 2", "line 3", "line 4", "line 5"}
	content := strings.Join(contentLines, "\n")
	err := os.WriteFile(testFile, []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("read entire file", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if result != content {
			t.Errorf("expected %q, got %q", content, result)
		}
	})

	t.Run("read head (first 2 lines)", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile, "line_end": 2})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "line 1\nline 2"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("read range (lines 2-4)", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile, "line_start": 2, "line_end": 4})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "line 2\nline 3\nline 4"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("read from line 3 to end", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile, "line_start": 3})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "line 3\nline 4\nline 5"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("read tail (last 2 lines)", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile, "tail_lines": 2})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "line 4\nline 5"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("read more tail than exists", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile, "tail_lines": 10})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if result != content {
			t.Errorf("expected %q, got %q", content, result)
		}
	})

	t.Run("out of bounds line_start", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": testFile, "line_start": 10})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})
}

func TestLocalGlobFiles(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	registerLocalGlobFiles(r, tmpDir)

	err := os.WriteFile(filepath.Join(tmpDir, "file1.go"), []byte(""), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte(""), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"directory": ".", "query": ".go"})
	result, err := r.HandleCall(context.Background(), llm.ToolCall{
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
	registerLocalReadFile(r, "")

	t.Run("missing file", func(t *testing.T) {
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: `{"file_path":"non_existent_file.txt"}`,
		})
		if err == nil {
			t.Error("expected error for non-existent file, got nil")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
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
	registerLocalGlobFiles(r, "")

	t.Run("invalid directory", func(t *testing.T) {
		// filepath.WalkDir doesn't necessarily error if the root doesn't exist depending on OS,
		// but we can test malformed JSON.
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "glob_files",
			Arguments: `{"query": 123}`,
		})
		if err == nil {
			t.Error("expected error for malformed JSON query, got nil")
		}
	})
}

func TestLocalGrepFiles(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	registerLocalGrepFiles(r, tmpDir, util.DefaultLockFiles)

	setupGitRepo(t, tmpDir)

	// Create a file with some content
	testFile := filepath.Join(tmpDir, "grep_test.txt")
	err := os.WriteFile(testFile, []byte("Hello Cassandra\nThis is a grep test.\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, tmpDir, "add", "grep_test.txt")

	t.Run("basic grep", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "Cassandra"})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
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
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
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
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
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
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
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
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
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
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
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

	t.Run("case-insensitive grep", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "cassandra", "case_insensitive": true})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if !strings.Contains(result, "grep_test.txt:1:7:Hello Cassandra") {
			t.Errorf("expected result to contain match (case-insensitive), got: %s", result)
		}
	})

	t.Run("truncation limit", func(t *testing.T) {
		truncFile := filepath.Join(tmpDir, "truncation.txt")
		var sb strings.Builder
		for i := 0; i < 2000; i++ {
			sb.WriteString(fmt.Sprintf("TruncMatch line %d\n", i))
		}
		if err := os.WriteFile(truncFile, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "truncation.txt")

		args, _ := json.Marshal(map[string]any{"query": "TruncMatch"})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name: "grep_files",

			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}

		// Verify result is truncated to 40k bytes (plus message)
		if len(result) > 42000 {
			t.Errorf("result is too large: %d bytes", len(result))
		}
		if !strings.Contains(result, "truncated to 40k bytes") {
			t.Errorf("expected truncation message, got: %s", result)
		}

		lines := strings.Split(strings.TrimSpace(result), "\n")
		if len(lines) < 2 {
			t.Fatalf("expected multiple lines, got: %v", lines)
		}
		matchLines := lines[:len(lines)-1]

		// Parse the remaining matches from the truncation message:
		// "... (truncated to 40k bytes, there are 84 more matches. Please refine your query)"
		lastLine := lines[len(lines)-1]
		var remainingMatches int
		_, err = fmt.Sscanf(lastLine, "... (truncated to 40k bytes, there are %d more matches. Please refine your query)", &remainingMatches)
		if err != nil {
			t.Fatalf("failed to parse remaining matches from last line %q: %v", lastLine, err)
		}

		totalMatches := len(matchLines) + remainingMatches
		if totalMatches != 2000 {
			t.Errorf("expected total matches to sum to 2000, got %d (returned) + %d (remaining) = %d", len(matchLines), remainingMatches, totalMatches)
		}
	})

	t.Run("scan truncation limit", func(t *testing.T) {
		truncFile := filepath.Join(tmpDir, "scan_truncation.txt")
		var sb strings.Builder
		// Write 6000 lines of ~25 bytes each (~150 KB), exceeding the 100 KB scan limit.
		for i := 0; i < 6000; i++ {
			sb.WriteString(fmt.Sprintf("ScanTruncMatch line %d\n", i))
		}
		if err := os.WriteFile(truncFile, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "scan_truncation.txt")

		args, _ := json.Marshal(map[string]any{"query": "ScanTruncMatch"})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(result, "there are many more matches") {
			t.Errorf("expected 'there are many more matches' in truncation message indicating scan limit was reached, got: %s", result)
		}
	})

	t.Run("grep with glob pattern", func(t *testing.T) {
		subdir := filepath.Join(tmpDir, "globdir")
		if err := os.Mkdir(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		matchFile := filepath.Join(subdir, "match.txt")
		if err := os.WriteFile(matchFile, []byte("GlobMatch here"), 0o644); err != nil {
			t.Fatal(err)
		}
		noMatchFile := filepath.Join(subdir, "nomatch.go")
		if err := os.WriteFile(noMatchFile, []byte("GlobMatch here too but wrong ext"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, tmpDir, "add", "globdir/match.txt", "globdir/nomatch.go")

		args, _ := json.Marshal(map[string]any{"query": "GlobMatch", "directory": "globdir/*.txt"})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if !strings.Contains(result, "globdir/match.txt") {
			t.Errorf("expected match in globdir/match.txt, got: %s", result)
		}
		if strings.Contains(result, "globdir/nomatch.go") {
			t.Errorf("expected no match in globdir/nomatch.go, got: %s", result)
		}
	})

	t.Run("grep with glob pattern and no directory base", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "GlobMatch", "directory": "*.txt"})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if !strings.Contains(result, "globdir/match.txt") {
			t.Errorf("expected match in globdir/match.txt, got: %s", result)
		}
	})

	t.Run("grep path traversal prevention with glob", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "GlobMatch", "directory": "../**/*.txt"})
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err == nil {
			t.Error("expected error for path traversal directory glob pattern, got nil")
		}
	})
}

func TestSplitGlob(t *testing.T) {
	tests := []struct {
		input       string
		wantBase    string
		wantPattern string
	}{
		{"", "", ""},
		{"dir/sub", "dir/sub", ""},
		{"*.go", "", "*.go"},
		{"dir/**/*.go", "dir", "**/*.go"},
		{"dir/file[0-9].txt", "dir", "file[0-9].txt"},
		{"[abc]/file", "", "[abc]/file"},
		{"dir\\sub\\*.go", "dir\\sub", "*.go"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			gotBase, gotPattern := splitGlob(tc.input)
			if gotBase != tc.wantBase || gotPattern != tc.wantPattern {
				t.Errorf("splitGlob(%q) = (%q, %q); want (%q, %q)",
					tc.input, gotBase, gotPattern, tc.wantBase, tc.wantPattern)
			}
		})
	}
}
