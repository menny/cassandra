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

func TestValidatePathInRoot(t *testing.T) {
	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Resolve root to handle macOS /var symlink
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	// Create a secret file outside root
	secretFile := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("valid path", func(t *testing.T) {
		path := filepath.Join(root, "file.txt")
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ValidatePathInRoot(root, "file.txt")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		rel, _ := filepath.Rel(root, got)
		if rel != "file.txt" {
			t.Errorf("expected rel path 'file.txt', got %q", rel)
		}
	})

	t.Run("traversal attempt", func(t *testing.T) {
		_, err := ValidatePathInRoot(root, "../secret.txt")
		if err == nil {
			t.Error("expected error for traversal, got nil")
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(secretFile, link); err != nil {
			t.Fatal(err)
		}
		_, err := ValidatePathInRoot(root, "link")
		if err == nil {
			t.Error("expected error for symlink escape, got nil")
		}
	})

	t.Run("nested symlink escape", func(t *testing.T) {
		subdir := filepath.Join(tmpDir, "outside_dir")
		if err := os.Mkdir(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		outsideFile := filepath.Join(subdir, "file.txt")
		if err := os.WriteFile(outsideFile, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(root, "dir_link")
		if err := os.Symlink(subdir, link); err != nil {
			t.Fatal(err)
		}

		_, err := ValidatePathInRoot(root, "dir_link/file.txt")
		if err == nil {
			t.Error("expected error for nested symlink escape, got nil")
		}
	})

	t.Run("valid symlink within root", func(t *testing.T) {
		target := filepath.Join(root, "target.txt")
		if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "valid_link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		got, err := ValidatePathInRoot(root, "valid_link")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		rel, _ := filepath.Rel(root, got)
		if rel != "target.txt" {
			t.Errorf("expected rel path 'target.txt', got %q", rel)
		}
	})

	t.Run("nested non-existent symlink escape", func(t *testing.T) {
		link := filepath.Join(root, "bad_link")
		if err := os.Symlink(tmpDir, link); err != nil {
			t.Fatal(err)
		}
		// This should fail even though 'a/b/c.txt' does not exist
		_, err := ValidatePathInRoot(root, "bad_link/a/b/c.txt")
		if err == nil {
			t.Error("expected error for nested non-existent symlink escape, got nil")
		}
	})
}

func TestOpenInRoot(t *testing.T) {
	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("valid path", func(t *testing.T) {
		f, err := OpenInRoot(root, "test.txt")
		if err != nil {
			t.Fatalf("OpenInRoot failed: %v", err)
		}
		f.Close()
	})

	t.Run("invalid path", func(t *testing.T) {
		_, err := OpenInRoot(root, "../secret.txt")
		if err == nil {
			t.Fatal("expected error for invalid path, got nil")
		}
	})

	t.Run("no root", func(t *testing.T) {
		f, err := OpenInRoot("", filePath)
		if err != nil {
			t.Fatalf("OpenInRoot failed with no root: %v", err)
		}
		f.Close()
	})
}

func TestSafeRel(t *testing.T) {
	t.Run("valid rel", func(t *testing.T) {
		got := SafeRel("/a/b", "/a/b/c")
		if got != "c" {
			t.Errorf("expected 'c', got %q", got)
		}
	})

	t.Run("invalid rel", func(t *testing.T) {
		// On windows this might fail if they are on different drives,
		// but on unix it usually works. To force an error, maybe empty base?
		// Actually filepath.Rel("", "a") might error.
		got := SafeRel("", "a")
		if got != "a" {
			t.Errorf("expected 'a' on error, got %q", got)
		}
	})
}

func TestValidateAndRel(t *testing.T) {
	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	filePath := filepath.Join(root, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("valid path", func(t *testing.T) {
		got, err := ValidateAndRel(root, "test.txt")
		if err != nil {
			t.Fatalf("ValidateAndRel failed: %v", err)
		}
		if got != "test.txt" {
			t.Errorf("expected 'test.txt', got %q", got)
		}
	})

	t.Run("invalid path", func(t *testing.T) {
		_, err := ValidateAndRel(root, "../secret.txt")
		if err == nil {
			t.Fatal("expected error for invalid path, got nil")
		}
	})
}
