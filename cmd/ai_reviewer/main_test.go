package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/menny/cassandra/core"
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

func TestResolveGuidelinesContent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a local file
	localFile := filepath.Join(tmpDir, "my_rules.md")
	localContent := "local rules content"
	require.NoError(t, os.WriteFile(localFile, []byte(localContent), 0o644))

	t.Run("resolves local file path", func(t *testing.T) {
		content, err := resolveGuidelinesContent(localFile)
		require.NoError(t, err)
		require.Equal(t, localContent, content)
	})

	t.Run("resolves named prompt from embedded library", func(t *testing.T) {
		content, err := resolveGuidelinesContent("google")
		require.NoError(t, err)
		require.Contains(t, content, "Google Engineering Practices")
	})

	t.Run("fails on non-existent path and name", func(t *testing.T) {
		_, err := resolveGuidelinesContent("non-existent-at-all")
		require.Error(t, err)
		require.Contains(t, err.Error(), "prompt \"non-existent-at-all\" not found in library")
	})
}
