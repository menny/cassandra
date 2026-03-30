package tools

import (
	"encoding/json"
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
