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

	t.Run("default cassandra-config-folder", func(t *testing.T) {
		cfg := NewDefaultConfig()
		if cfg.CassandraConfigFolder != "~/.cassandra" {
			t.Errorf("expected default CassandraConfigFolder to be ~/.cassandra, got %q", cfg.CassandraConfigFolder)
		}
	})

	t.Run("unmarshal cassandra-config-folder", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
provider = "google"
model = "gemini-3.1-pro-preview"
cassandra-config-folder = "/custom/path"
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.CassandraConfigFolder != "/custom/path" {
			t.Errorf("expected CassandraConfigFolder to be /custom/path, got %q", cfg.CassandraConfigFolder)
		}
	})

	t.Run("unmarshal cassandraConfigFolder alias", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
provider = "google"
model = "gemini-3.1-pro-preview"
cassandraConfigFolder = "/alias/path"
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.CassandraConfigFolder != "/alias/path" {
			t.Errorf("expected CassandraConfigFolder to be /alias/path, got %q", cfg.CassandraConfigFolder)
		}
	})

	t.Run("default t3 config", func(t *testing.T) {
		cfg := NewDefaultConfig()
		if len(cfg.T3.PreferredHarnesses) == 0 || cfg.T3.PreferredHarnesses[0] != "antigravity" {
			t.Errorf("expected default PreferredHarnesses starting with antigravity, got %v", cfg.T3.PreferredHarnesses)
		}
		if !cfg.T3.PromptForHarness {
			t.Errorf("expected default PromptForHarness to be true")
		}
	})

	t.Run("unmarshal t3 config", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
provider = "google"
model = "gemini-3.1-pro-preview"

[t3]
preferred-harnesses = ["opencode", "antigravity"]
preferred-model = "gemini-3.8-flash-high"
prompt-for-harness = false
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if len(cfg.T3.PreferredHarnesses) != 2 || cfg.T3.PreferredHarnesses[0] != "opencode" || cfg.T3.PreferredHarnesses[1] != "antigravity" {
			t.Errorf("unexpected preferred harnesses: %v", cfg.T3.PreferredHarnesses)
		}
		if cfg.T3.PreferredModel != "gemini-3.8-flash-high" {
			t.Errorf("expected preferred-model to be gemini-3.8-flash-high, got %q", cfg.T3.PreferredModel)
		}
		if cfg.T3.PromptForHarness {
			t.Errorf("expected prompt-for-harness to be false")
		}
	})

	t.Run("default omnigent config", func(t *testing.T) {
		cfg := NewDefaultConfig()
		if len(cfg.Omnigent.PreferredHarnesses) == 0 || cfg.Omnigent.PreferredHarnesses[0] != "antigravity-native-ui" {
			t.Errorf("expected default PreferredHarnesses starting with antigravity-native-ui, got %v", cfg.Omnigent.PreferredHarnesses)
		}
		if !cfg.Omnigent.PromptForHarness {
			t.Errorf("expected default PromptForHarness to be true")
		}
	})

	t.Run("unmarshal omnigent config", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cassandra.toml")
		content := `
provider = "google"
model = "gemini-3.1-pro-preview"

[omnigent]
preferred-harnesses = ["opencode-native-ui", "claude-native-ui"]
preferred-model = "gemini-3.8-flash-high"
prompt-for-harness = false
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if len(cfg.Omnigent.PreferredHarnesses) != 2 || cfg.Omnigent.PreferredHarnesses[0] != "opencode-native-ui" || cfg.Omnigent.PreferredHarnesses[1] != "claude-native-ui" {
			t.Errorf("unexpected preferred harnesses: %v", cfg.Omnigent.PreferredHarnesses)
		}
		if cfg.Omnigent.PreferredModel != "gemini-3.8-flash-high" {
			t.Errorf("expected preferred-model to be gemini-3.8-flash-high, got %q", cfg.Omnigent.PreferredModel)
		}
		if cfg.Omnigent.PromptForHarness {
			t.Errorf("expected prompt-for-harness to be false")
		}
	})
}
