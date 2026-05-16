package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/viper"

	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/tools"
)

// Config represents the complete configuration for a Cassandra reviewer.
type Config struct {
	Base                         string   `mapstructure:"base"`
	Head                         string   `mapstructure:"head"`
	Model                        string   `mapstructure:"model"`
	Provider                     string   `mapstructure:"provider"`
	ProviderAPIKey               string   `mapstructure:"provider-api-key"`
	ProviderURL                  string   `mapstructure:"provider-url"`
	WorkingDir                   string   `mapstructure:"-"`
	MainGuidelines               string   `mapstructure:"main-guidelines"`
	SupplementalGuidelines       []string `mapstructure:"supplemental-guidelines"`
	MaxTokens                    int      `mapstructure:"max-tokens"`
	ReviewOutputFile             string   `mapstructure:"review-output-file"`
	OutputJSONFile               string   `mapstructure:"output-json"`
	MetricsJSONFile              string   `mapstructure:"metrics-json"`
	ExtractionModel              string   `mapstructure:"extraction-model"`
	MetadataJSONFile             string   `mapstructure:"metadata-json"`
	ApprovalEvaluationPromptFile string   `mapstructure:"approval-evaluation-prompt-file"`
	DiffFile                     string   `mapstructure:"diff-file"`
	FilesListFile                string   `mapstructure:"files-list-file"`
	CommitsFile                  string   `mapstructure:"commits-file"`
	MCPConfigFile                string   `mapstructure:"mcp-config"`
	AllowURLFetch                bool     `mapstructure:"allow-url-fetch"`
	IgnoredLockFiles             []string `mapstructure:"ignored-lock-files"`
	ConfigFile                   string   `mapstructure:"config"`
	WishlistDir                  string   `mapstructure:"wishlist-dir"`
}

// NewDefaultConfig returns a Config with default values populated.
func NewDefaultConfig() *Config {
	return &Config{
		Base:             "main",
		Head:             "HEAD",
		MainGuidelines:   "general",
		MaxTokens:        llm.DefaultMaxTokens,
		IgnoredLockFiles: tools.DefaultLockFiles,
	}
}

// Load reads the configuration from a TOML file.
// It does NOT handle CLI flags or environment variables; the caller should bind those
// if needed before calling Unmarshal.
func Load(configFile string) (*Config, error) {
	v := viper.New()
	v.SetDefault("main-guidelines", "general")
	v.SetDefault("base", "main")
	v.SetDefault("head", "HEAD")
	v.SetDefault("max-tokens", llm.DefaultMaxTokens)
	v.SetDefault("ignored-lock-files", tools.DefaultLockFiles)
	v.SetDefault("allow-url-fetch", false)

	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("cassandra")
		v.SetConfigType("toml")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		if configFile != "" {
			return nil, fmt.Errorf("failed to read config file %q: %w", configFile, err)
		}
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	return cfg, nil
}

// ResolveGuidelinesContent fetches the content of a guideline, either from a file or the library.
func ResolveGuidelinesContent(guidelinesPath string) (string, error) {
	// Try the path as provided first
	if content, err := os.ReadFile(guidelinesPath); err == nil {
		return string(content), nil
	}

	// Try as a named prompt in the library (embedded)
	return prompts.GetLibraryPrompt(guidelinesPath)
}
