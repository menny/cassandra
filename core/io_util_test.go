package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileWithDirs(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("creates missing parent directories", func(t *testing.T) {
		targetPath := filepath.Join(tempDir, "a/b/c/test.txt")
		data := []byte("hello world")

		err := WriteFileWithDirs(targetPath, data)
		if err != nil {
			t.Fatalf("WriteFileWithDirs failed: %v", err)
		}

		// Verify file exists and has correct content
		content, err := os.ReadFile(targetPath)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}
		if string(content) != "hello world" {
			t.Errorf("got content %q, want %q", string(content), "hello world")
		}
	})

	t.Run("works when directory already exists", func(t *testing.T) {
		targetPath := filepath.Join(tempDir, "existing/test.txt")
		err := os.MkdirAll(filepath.Dir(targetPath), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		data := []byte("data")
		err = WriteFileWithDirs(targetPath, data)
		if err != nil {
			t.Fatalf("WriteFileWithDirs failed: %v", err)
		}

		content, err := os.ReadFile(targetPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "data" {
			t.Errorf("got %q, want %q", string(content), "data")
		}
	})
}
