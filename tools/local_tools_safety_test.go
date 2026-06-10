package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/menny/cassandra/llm"
)

func TestLocalReadFile_SafetyLimits(t *testing.T) {
	r := NewRegistry()
	registerLocalReadFile(r, "")

	tmpDir := t.TempDir()

	t.Run("read whole file > 40000 bytes fails", func(t *testing.T) {
		largeFile := filepath.Join(tmpDir, "large_whole.txt")
		content := strings.Repeat("a", 40001)
		if err := os.WriteFile(largeFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"file_path": largeFile})
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err == nil {
			t.Error("expected error when reading file > 40000 bytes without line limits, but got nil")
		} else if !strings.Contains(err.Error(), "too large") {
			t.Errorf("expected 'too large' error, got: %v", err)
		}
	})

	t.Run("read whole file <= 40000 bytes succeeds", func(t *testing.T) {
		smallFile := filepath.Join(tmpDir, "small_whole.txt")
		content := strings.Repeat("a", 40000)
		if err := os.WriteFile(smallFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"file_path": smallFile})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if len(result) != 40000 {
			t.Errorf("expected 40000 bytes, got %d", len(result))
		}
	})

	t.Run("tail_lines is clamped to 10000", func(t *testing.T) {
		largeTailFile := filepath.Join(tmpDir, "large_tail.txt")
		f, err := os.Create(largeTailFile)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20000; i++ {
			if _, err := f.WriteString("line\n"); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()

		args, _ := json.Marshal(map[string]any{"file_path": largeTailFile, "tail_lines": 1000000})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		// The result should have at most 10000 lines.
		// 10000 lines + possibly a truncation message if it hit byte limit too,
		// but here 10000 lines * 5 bytes ("line\n") = 50KB, which is > 40KB.
		// So it should be limited by 40KB.
		if len(result) > 41000 {
			t.Errorf("result too large: %d", len(result))
		}
	})

	t.Run("handles very long lines gracefully", func(t *testing.T) {
		longLineFile := filepath.Join(tmpDir, "long_line.txt")
		// Create a line longer than the previous 64KB Scanner limit.
		content := strings.Repeat("a", 70000) + "\n"
		if err := os.WriteFile(longLineFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"file_path": longLineFile, "line_start": 1})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatalf("expected success with long line, got: %v", err)
		}
		// It should be truncated to maxOutput (40000).
		if len(result) > 41000 {
			t.Errorf("result too large: %d", len(result))
		}
		if !strings.Contains(result, "truncated") {
			t.Error("expected truncation message")
		}
	})

	t.Run("preserves empty lines in range", func(t *testing.T) {
		emptyLinesFile := filepath.Join(tmpDir, "empty_lines.txt")
		content := "\nline 2\n\nline 4\n"
		if err := os.WriteFile(emptyLinesFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"file_path": emptyLinesFile, "line_start": 1})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "\nline 2\n\nline 4"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("output truncated at 40000 bytes", func(t *testing.T) {
		overflowFile := filepath.Join(tmpDir, "overflow.txt")
		content := strings.Repeat("a\n", 30000) // 60,000 bytes
		if err := os.WriteFile(overflowFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"file_path": overflowFile, "line_start": 1})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) > 41000 {
			t.Errorf("output too large: %d bytes", len(result))
		}
		if !strings.Contains(result, "truncated") {
			t.Error("expected truncation message in output")
		}
	})

	t.Run("RespectsMaxReadLength", func(t *testing.T) {
		smallFile := filepath.Join(tmpDir, "max_len.txt")
		content := "1234567890" + strings.Repeat("a", 100)
		if err := os.WriteFile(smallFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		// Use a limit that is larger than the truncation suffix (approx 16 bytes)
		// but smaller than the total content.
		args, _ := json.Marshal(map[string]any{"file_path": smallFile, "max_read_length": 30})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result, "1234567890") {
			t.Errorf("expected prefix 1234567890, got %q", result)
		}
		if !strings.Contains(result, "truncated") {
			t.Error("expected truncation message in output")
		}
		if len(result) > 30 {
			t.Errorf("expected result length <= 30, got %d", len(result))
		}
	})
}

func TestLocalReadFile_SymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()
	workspaceRoot := filepath.Join(tmpDir, "workspace")
	if err := os.Mkdir(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	RegisterLocalTools(r, workspaceRoot, nil, "")

	// Create a file OUTSIDE the workspace root
	secretFile := filepath.Join(tmpDir, "secret.txt")
	secretContent := "SENSITIVE SYSTEM DATA"
	if err := os.WriteFile(secretFile, []byte(secretContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink INSIDE the workspace root pointing OUTSIDE
	linkPath := filepath.Join(workspaceRoot, "malicious_link")
	if err := os.Symlink(secretFile, linkPath); err != nil {
		t.Fatal(err)
	}

	t.Run("symlink escape should be blocked", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"file_path": "malicious_link"})
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})

		if err == nil {
			t.Error("Security Vulnerability: read_file successfully read content through a symlink escaping the workspace root")
		}
	})
}

func TestLocalGrepFiles_SymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()
	workspaceRoot := filepath.Join(tmpDir, "workspace")
	if err := os.Mkdir(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	RegisterLocalTools(r, workspaceRoot, nil, "")

	// Create a file OUTSIDE the workspace root
	secretDir := filepath.Join(tmpDir, "outside")
	if err := os.Mkdir(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("SENSITIVE DATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Valid symlink pointing outside workspace root
	evilSymlink := filepath.Join(workspaceRoot, "evil_symlink")
	if err := os.Symlink(secretDir, evilSymlink); err != nil {
		t.Fatal(err)
	}

	// 2. Broken symlink
	brokenSymlink := filepath.Join(workspaceRoot, "broken_symlink")
	if err := os.Symlink(filepath.Join(tmpDir, "nonexistent"), brokenSymlink); err != nil {
		t.Fatal(err)
	}

	// 3. Trampoline/circular symlink
	trampolineSymlink := filepath.Join(workspaceRoot, "trampoline")
	if err := os.Symlink(trampolineSymlink, trampolineSymlink); err != nil {
		t.Fatal(err)
	}

	t.Run("valid symlink pointing outside should be blocked", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "SENSITIVE", "directory": "evil_symlink/*.txt"})
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err == nil {
			t.Error("expected error when querying through outside symlink base directory, got nil")
		}
	})

	t.Run("broken symlink should be blocked", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "SENSITIVE", "directory": "broken_symlink/*.txt"})
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err == nil {
			t.Error("expected error when querying through broken symlink base directory, got nil")
		}
	})

	t.Run("trampoline symlink should be blocked", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"query": "SENSITIVE", "directory": "trampoline/*.txt"})
		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "grep_files",
			Arguments: string(args),
		})
		if err == nil {
			t.Error("expected error when querying through circular trampoline symlink base directory, got nil")
		}
	})
}
