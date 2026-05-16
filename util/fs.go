package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteFileWithDirs creates any missing parent directories before writing the
// file with 0o644 permissions.
func WriteFileWithDirs(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write file to %s: %w", path, err)
	}
	return nil
}

// SanitizeFileName replaces any non-alphanumeric character with an underscore.
func SanitizeFileName(s string) string {
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}

// SanitizeFileNameWithDefault sanitizes a filename and returns a default value
// if the result is empty or consists only of underscores.
func SanitizeFileNameWithDefault(s, defaultName string) string {
	res := SanitizeFileName(s)
	if strings.Trim(res, "_") == "" {
		return defaultName
	}
	return res
}
