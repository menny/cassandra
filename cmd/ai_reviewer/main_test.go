package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/menny/cassandra/core"
	"github.com/menny/cassandra/core/config"
	"github.com/stretchr/testify/require"
)

func TestConfigPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	tomlPath := filepath.Join(tmpDir, "cassandra.toml")
	tomlContent := `
model = "config-model"
provider = "config-provider"
max-tokens = 100
`
	require.NoError(t, os.WriteFile(tomlPath, []byte(tomlContent), 0o644))

	t.Run("CLI takes precedence over config", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("model", "", "")
		fs.String("provider", "", "")
		fs.Int("max-tokens", 0, "")

		v := viper.New()
		v.SetDefault("max-tokens", 50)

		// Simulate CLI flag
		require.NoError(t, fs.Set("model", "cli-model"))

		// Only bind changed flags
		fs.VisitAll(func(f *flag.Flag) {
			if f.Changed {
				require.NoError(t, v.BindPFlag(f.Name, f))
			}
		})

		v.SetConfigFile(tomlPath)
		require.NoError(t, v.ReadInConfig())

		require.Equal(t, "cli-model", v.GetString("model"))
		require.Equal(t, "config-provider", v.GetString("provider"))
		require.Equal(t, 100, v.GetInt("max-tokens"))
	})

	t.Run("Config takes precedence over default", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("model", "default-model", "")

		v := viper.New()
		v.SetDefault("model", "viper-default")

		// Only bind changed flags (none changed here)
		fs.VisitAll(func(f *flag.Flag) {
			if f.Changed {
				require.NoError(t, v.BindPFlag(f.Name, f))
			}
		})

		v.SetConfigFile(tomlPath)
		require.NoError(t, v.ReadInConfig())

		require.Equal(t, "config-model", v.GetString("model"))
	})
}

func TestFormatMetadata(t *testing.T) {
	now := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	metadata := core.PRMetadata{
		RepoFullName: "owner/repo",
		Author:       "author1",
		CreatedAt:    now,
		Title:        "PR Title",
		Description:  "PR Description",
		Comments: []core.PRComment{
			{
				Author: "user1",
				Body:   "comment 1",
				Date:   now.Add(time.Hour),
				IsSelf: false,
			},
			{
				Author:    "user2",
				Body:      "block comment",
				Date:      now.Add(90 * time.Minute),
				IsSelf:    false,
				Path:      "file.go",
				StartLine: 10,
				Line:      20,
			},
			{
				Author: "user3",
				Body:   "file comment",
				Date:   now.Add(100 * time.Minute),
				IsSelf: false,
				Path:   "README.md",
				Line:   0,
			},
			{
				Author: "cassandra",
				Body:   "comment 2",
				Date:   now.Add(2 * time.Hour),
				IsSelf: true,
			},
		},
	}

	formatted := formatMetadata(metadata)

	require.Contains(t, formatted, "### PR Metadata")
	require.Contains(t, formatted, "- **Repository**: owner/repo")
	require.Contains(t, formatted, "- **Author**: author1")
	require.Contains(t, formatted, "- **Date**: 2026-04-09")
	require.Contains(t, formatted, "- **Title**: PR Title")
	require.Contains(t, formatted, "- **Description**: PR Description")

	require.Contains(t, formatted, "### PR Comments")
	require.Contains(t, formatted, "- **user1** (2026-04-09 11:00):")
	require.Contains(t, formatted, "  > comment 1")
	require.Contains(t, formatted, "- **user2** (2026-04-09 11:30) on file.go:10-20:")
	require.Contains(t, formatted, "  > block comment")
	require.Contains(t, formatted, "- **user3** (2026-04-09 11:40) on README.md (file-level):")
	require.Contains(t, formatted, "  > file comment")
	require.Contains(t, formatted, "- **cassandra (Cassandra Bot)** (2026-04-09 12:00):")
	require.Contains(t, formatted, "  > comment 2")
}

func TestRun_ConfigDiscovery(t *testing.T) {
	ctx := context.Background()
	stderr := log.New(os.Stderr, "", 0)

	t.Run("silently ignores missing default cassandra.toml", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		defer func() { _ = os.Chdir(cwd) }()

		tmpDir := t.TempDir()
		t.Setenv("BUILD_WORKSPACE_DIRECTORY", tmpDir)

		// We provide just enough flags to trigger the "No changes found" exit path
		// (which happens after config loading).
		args := []string{
			"--provider", "google",
			"--model", "gemini-1.5-flash",
			"--provider-api-key", "fake-key",
			"--diff-file", "non-existent-diff",
			"--files-list-file", "non-existent-files",
		}

		err = run(ctx, args, stderr)
		require.Error(t, err)
		// It should fail on reading the diff file, NOT on reading the config file
		require.Contains(t, err.Error(), "failed to read diff file")
	})

	t.Run("errors when explicit --config is missing", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		defer func() { _ = os.Chdir(cwd) }()

		tmpDir := t.TempDir()
		t.Setenv("BUILD_WORKSPACE_DIRECTORY", tmpDir)

		args := []string{
			"--config", "missing-config.toml",
			"--provider", "google",
			"--model", "gemini-1.5-flash",
			"--provider-api-key", "fake-key",
		}

		err = run(ctx, args, stderr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read config file \"missing-config.toml\"")
	})

	t.Run("errors when required arguments are missing", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		defer func() { _ = os.Chdir(cwd) }()

		tmpDir := t.TempDir()
		t.Setenv("BUILD_WORKSPACE_DIRECTORY", tmpDir)

		args := []string{
			"--provider", "google",
			// --model and --provider-api-key missing
		}

		err = run(ctx, args, stderr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing required arguments")
		require.Contains(t, err.Error(), "--model")
		require.Contains(t, err.Error(), "--provider-api-key")
	})
}

func TestResolveGuidelinesContent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a local file
	localFile := filepath.Join(tmpDir, "my_rules.md")
	localContent := "local rules content"
	require.NoError(t, os.WriteFile(localFile, []byte(localContent), 0o644))

	t.Run("resolves local file path", func(t *testing.T) {
		content, err := config.ResolveGuidelinesContent(localFile)
		require.NoError(t, err)
		require.Equal(t, localContent, content)
	})

	t.Run("resolves named prompt from embedded library", func(t *testing.T) {
		content, err := config.ResolveGuidelinesContent("google")
		require.NoError(t, err)
		require.Contains(t, content, "Google Engineering Practices")
	})

	t.Run("fails on non-existent path and name", func(t *testing.T) {
		_, err := config.ResolveGuidelinesContent("non-existent-at-all")
		require.Error(t, err)
		require.Contains(t, err.Error(), "prompt \"non-existent-at-all\" not found in library")
	})
}

func TestRun_AllowAskDeveloperValidation(t *testing.T) {
	ctx := context.Background()
	stderr := log.New(os.Stderr, "", 0)

	t.Run("errors when allow-ask-developer is true but render is raw", func(t *testing.T) {
		args := []string{
			"--provider", "google",
			"--model", "gemini-1.5-flash",
			"--provider-api-key", "fake-key",
			"--allow-ask-developer",
			"--render", "raw",
		}

		err := run(ctx, args, stderr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "--allow-ask-developer can only be used when --render is 'markdown' or 'tui'")
	})

	t.Run("proceeds past validation when allow-ask-developer is true and render is markdown", func(t *testing.T) {
		args := []string{
			"--provider", "google",
			"--model", "gemini-1.5-flash",
			"--provider-api-key", "fake-key",
			"--allow-ask-developer",
			"--render", "markdown",
			"--diff-file", "non-existent-diff",
			"--files-list-file", "non-existent-files",
		}

		err := run(ctx, args, stderr)
		require.Error(t, err)
		// It should pass the validation check and fail on reading the non-existent diff file.
		require.Contains(t, err.Error(), "failed to read diff file")
	})
}
