package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	result, err := r.HandleCall("read_file", map[string]any{"file_path": testFile})
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

	result, err := r.HandleCall("glob_files", map[string]any{
		"directory": tmpDir,
		"query":     ".go",
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
