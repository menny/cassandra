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

func TestWishlistTool(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRegistry()
	registerWishlistTool(r, tmpDir)

	t.Run("record wishlist entry", func(t *testing.T) {
		args := wishlistArgs{
			ToolName:    "test_tool",
			Description: "A test tool description",
			Rationale:   "Testing the wishlist tool",
		}
		argsJSON, _ := json.Marshal(args)

		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}

		if !strings.Contains(result, "Wishlist entry recorded to") {
			t.Errorf("unexpected result: %s", result)
		}

		// Verify file exists and content is correct
		files, err := filepath.Glob(filepath.Join(tmpDir, "wish_test_tool_*_*.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("expected 1 wishlist file, found %d", len(files))
		}

		data, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}

		var recorded wishlistArgs
		if err := json.Unmarshal(data, &recorded); err != nil {
			t.Fatalf("failed to unmarshal recorded data: %v", err)
		}

		if recorded.ToolName != args.ToolName || recorded.Description != args.Description || recorded.Rationale != args.Rationale {
			t.Errorf("recorded data does not match input. Got %+v, want %+v", recorded, args)
		}
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		argsJSON, _ := json.Marshal(wishlistArgs{
			ToolName:    "cancelled_tool",
			Description: "desc",
			Rationale:   "rat",
		})

		_, err := r.HandleCall(ctx, llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err == nil {
			t.Error("expected error for cancelled context, got nil")
		}
	})

	t.Run("prevents path traversal", func(t *testing.T) {
		argsJSON, _ := json.Marshal(wishlistArgs{
			ToolName:    "../../../evil",
			Description: "desc",
			Rationale:   "rat",
		})

		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(result, "wish__________evil_") {
			t.Errorf("expected sanitized filename '__________evil', got: %s", result)
		}
		if strings.Contains(result, "..") {
			t.Errorf("result still contains path traversal characters: %s", result)
		}
	})

	t.Run("handles extreme sanitization", func(t *testing.T) {
		argsJSON, _ := json.Marshal(wishlistArgs{
			ToolName:    "////",
			Description: "desc",
			Rationale:   "rat",
		})

		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(result, "wish_unnamed_tool_") {
			t.Errorf("expected 'unnamed_tool' for empty sanitization, got: %s", result)
		}
	})

	t.Run("truncates long tool names", func(t *testing.T) {
		longName := strings.Repeat("a", 100)
		argsJSON, _ := json.Marshal(wishlistArgs{
			ToolName:    longName,
			Description: "desc",
			Rationale:   "rat",
		})

		result, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err != nil {
			t.Fatal(err)
		}

		expectedPart := "wish_" + strings.Repeat("a", 64) + "_"
		if !strings.Contains(result, expectedPart) {
			t.Errorf("expected filename to be truncated to 64 chars, got: %s", result)
		}
	})

	t.Run("enforces input size limit", func(t *testing.T) {
		largeDesc := strings.Repeat("a", 65*1024)
		argsJSON, _ := json.Marshal(wishlistArgs{
			ToolName:    "large_tool",
			Description: largeDesc,
			Rationale:   "rat",
		})

		_, err := r.HandleCall(context.Background(), llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err == nil {
			t.Error("expected error for large input, got nil")
		}
		if !strings.Contains(err.Error(), "input exceeds maximum allowed size") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("fails when wishlist dir is empty", func(t *testing.T) {
		rEmpty := NewRegistry()
		registerWishlistTool(rEmpty, "")

		argsJSON, _ := json.Marshal(wishlistArgs{
			ToolName:    "fail_tool",
			Description: "desc",
			Rationale:   "rat",
		})

		_, err := rEmpty.HandleCall(context.Background(), llm.ToolCall{
			Name:      "wishlist_tool",
			Arguments: string(argsJSON),
		})
		if err == nil {
			t.Error("expected error when wishlistDir is empty, got nil")
		}
		if !strings.Contains(err.Error(), "wishlist_tool is currently disabled") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}
