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

	t.Run("respects max_read_length", func(t *testing.T) {
		smallFile := filepath.Join(tmpDir, "max_len.txt")
		content := "1234567890"
		if err := os.WriteFile(smallFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{"file_path": smallFile, "max_read_length": 5})
		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "read_file",
			Arguments: string(args),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result, "12345") {
			t.Errorf("expected prefix 12345, got %q", result)
		}
		if !strings.Contains(result, "truncated") {
			t.Error("expected truncation message in output")
		}
	})
}
