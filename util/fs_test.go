package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"space file", "space_file"},
		{"path/to/file", "path_to_file"},
		{"!@#$%^", "______"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := SanitizeFileName(tt.input); got != tt.expected {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSanitizeFileNameWithDefault(t *testing.T) {
	tests := []struct {
		input       string
		defaultName string
		expected    string
	}{
		{"valid", "default", "valid"},
		{"////", "default", "default"},
		{"", "default", "default"},
		{"____", "default", "default"},
	}

	for _, tt := range tests {
		if got := SanitizeFileNameWithDefault(tt.input, tt.defaultName); got != tt.expected {
			t.Errorf("SanitizeFileNameWithDefault(%q, %q) = %q, want %q", tt.input, tt.defaultName, got, tt.expected)
		}
	}
}

func TestWriteFileWithDirs(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "a/b/c/test.txt")
	data := []byte("hello")

	if err := WriteFileWithDirs(path, data); err != nil {
		t.Fatalf("WriteFileWithDirs failed: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(content) != "hello" {
		t.Errorf("expected 'hello', got %q", string(content))
	}
}
