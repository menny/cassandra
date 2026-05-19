package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

type wishlistArgs struct {
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Rationale   string `json:"rationale"`
}

func registerWishlistTool(r *Registry, wishlistDir string) {
	def := llm.ToolDef{
		Name: "wishlist_tool",
		Description: "Record a need for a tool or capability you currently lack. " +
			"CRITICAL: Use this ONLY if you cannot perform the task with your current toolset, " +
			"or if the current method is significantly inefficient/unreliable. " +
			"Describe what the tool should do and provide a rough list of the arguments you would expect it to take.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_name": map[string]any{
					"type":        "string",
					"description": "Proposed name for the tool.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "What the tool should do, including a rough list of arguments it would need. Markdown is allowed.",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "Why this is needed and what task is currently being blocked/impeded.",
				},
			},
			"required": []string{"tool_name", "description", "rationale"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args wishlistArgs) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		// Input size validation: Enforce a 64KB total limit for input fields.
		const maxInputSize = 64 * 1024
		totalSize := len(args.ToolName) + len(args.Description) + len(args.Rationale)
		if totalSize > maxInputSize {
			return "", fmt.Errorf("wishlist_tool: input exceeds maximum allowed size of %d bytes", maxInputSize)
		}

		if wishlistDir == "" {
			return "", fmt.Errorf("wishlist_tool is currently disabled because no wishlist directory has been configured. To enable this feature, the developer must provide the --wishlist-dir flag (or the equivalent Action/config setting) to Cassandra")
		}

		timestamp := time.Now().UTC().Format("20060102_150405")
		// Sanitize tool name: replace any non-alphanumeric character with underscore
		safeToolName := util.SanitizeFileNameWithDefault(args.ToolName, "unnamed_tool")
		// Truncate to avoid filesystem filename length limits
		if len(safeToolName) > 64 {
			safeToolName = safeToolName[:64]
		}

		// Add a short random suffix to prevent collisions within the same second
		randomBytes := make([]byte, 2)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", fmt.Errorf("failed to generate random suffix: %w", err)
		}
		randomSuffix := fmt.Sprintf("%x", randomBytes)
		filename := fmt.Sprintf("wish_%s_%s_%s.json", safeToolName, timestamp, randomSuffix)
		filePath := filepath.Join(wishlistDir, filename)

		data, err := json.MarshalIndent(args, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal wishlist entry: %w", err)
		}

		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := util.WriteFileWithDirs(filePath, data); err != nil {
			return "", err
		}

		return fmt.Sprintf("Wishlist entry recorded to %s", filePath), nil
	})
}
