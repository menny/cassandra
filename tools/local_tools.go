package tools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

func registerLocalReadFile(r *Registry) {
	def := llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
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
		},
	}

	r.RegisterTool(def, func(args map[string]any) (string, error) {
		pathRaw, ok := args["file_path"]
		if !ok {
			return "", fmt.Errorf("missing argument: file_path")
		}
		pathStr, ok := pathRaw.(string)
		if !ok {
			return "", fmt.Errorf("file_path must be a string")
		}

		b, err := os.ReadFile(pathStr)
		if err != nil {
			return "", fmt.Errorf("read_file failed: %w", err)
		}
		return string(b), nil
	})
}

func registerLocalGlobFiles(r *Registry) {
	def := llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
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
		},
	}

	r.RegisterTool(def, func(args map[string]any) (string, error) {
		queryRaw, ok := args["query"]
		if !ok {
			return "", fmt.Errorf("missing argument: query")
		}
		query, ok := queryRaw.(string)
		if !ok {
			return "", fmt.Errorf("query must be a string")
		}

		dir := "."
		if dirRaw, ok := args["directory"]; ok {
			if d, ok := dirRaw.(string); ok && d != "" {
				dir = d
			}
		}

		var matches []string
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // Skip errors reading specific files
			}
			if !d.IsDir() {
				// Simple substring match allows catching things like ".go" or "BUILD"
				if strings.Contains(filepath.Base(path), query) || strings.Contains(path, query) {
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
