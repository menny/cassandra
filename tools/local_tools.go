package tools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/menny/cassandra/llm"
)

func registerLocalReadFile(r *Registry) {
	def := llm.ToolDef{
		Name:        "read_file",
		Description: "Read the contents of a local file from the repository.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file to read.",
				},
			},
			"required": []string{"file_path"},
		},
	}

	r.RegisterTool(def, func(tc llm.ToolCall) (string, error) {
		var args struct {
			FilePath string `json:"file_path"`
		}
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}

		b, err := os.ReadFile(args.FilePath)
		if err != nil {
			return "", fmt.Errorf("read_file failed: %w", err)
		}
		return string(b), nil
	})
}

func registerLocalGlobFiles(r *Registry) {
	def := llm.ToolDef{
		Name:        "glob_files",
		Description: "Search for files within a directory matching an exact substring or simple glob suffix.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"directory": map[string]any{
					"type":        "string",
					"description": "The root directory to search in, defaults to '.' if empty.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "The substring or extension to match against filenames (e.g. '.go' or 'agent').",
				},
			},
			"required": []string{"query"},
		},
	}

	r.RegisterTool(def, func(tc llm.ToolCall) (string, error) {
		var args struct {
			Directory string `json:"directory"`
			Query     string `json:"query"`
		}
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}

		dir := args.Directory
		if dir == "" {
			dir = "."
		}

		var matches []string
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // Skip errors reading specific files
			}
			if !d.IsDir() {
				// Simple substring match allows catching things like ".go" or "BUILD"
				if strings.Contains(filepath.Base(path), args.Query) || strings.Contains(path, args.Query) {
					matches = append(matches, path)
				}
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("glob_files failed: %w", err)
		}

		if len(matches) == 0 {
			return "No matching files found.", nil
		}
		return strings.Join(matches, "\n"), nil
	})
}
