package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("unmarshal allow-url-fetch", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
provider = "google"
model = "gemini-3.1-pro-preview"
allow-url-fetch = true
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if !cfg.AllowURLFetch {
			t.Errorf("expected AllowURLFetch to be true, got false")
		}
	})

	t.Run("default allow-url-fetch is false", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
provider = "google"
model = "gemini-3.1-pro-preview"
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.AllowURLFetch {
			t.Errorf("expected AllowURLFetch to be false, got true")
		}
	})

	t.Run("trims ignored-lock-files", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
ignored-lock-files = [" go.sum ", " package-lock.json\t"]
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		expected := []string{"go.sum", "package-lock.json"}
		if len(cfg.IgnoredLockFiles) != len(expected) {
			t.Fatalf("expected %d ignored lock files, got %d", len(expected), len(cfg.IgnoredLockFiles))
		}
		for i, v := range expected {
			if cfg.IgnoredLockFiles[i] != v {
				t.Errorf("expected IgnoredLockFiles[%d] to be %q, got %q", i, v, cfg.IgnoredLockFiles[i])
			}
		}
	})
}
