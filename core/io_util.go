package core

import (
	"fmt"
	"os"
	"path/filepath"
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
