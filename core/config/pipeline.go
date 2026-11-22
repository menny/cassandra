package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DefaultPipelineMaxConcurrency caps concurrent static pipeline subagents unless
// the pipeline file overrides it, and concurrent dynamic subagents when no
// pipeline is configured.
const DefaultPipelineMaxConcurrency = 4

// ReviewPipeline describes the subagents to run before the main reviewer.
type ReviewPipeline struct {
	MaxConcurrency int        `toml:"max_concurrency"`
	Subagents      []Subagent `toml:"subagents"`
}

// Subagent is a validated pipeline step; Prompt contains the eagerly loaded file content.
type Subagent struct {
	Title         string       `toml:"title"`
	Description   string       `toml:"description"`
	PromptFile    string       `toml:"prompt_file"`
	Required      bool         `toml:"required"`
	UserVisible   bool         `toml:"user_visible"`
	ReviewerInput bool         `toml:"reviewer_input"`
	AllowMCPs     bool         `toml:"allow_mcps"`
	LLM           LLMOverrides `toml:"llm"`
	Prompt        string       `toml:"-"`
}

// LLMOverrides distinguishes omitted settings from explicit empty string overrides.
// ProviderOptions replaces matching top-level keys rather than merging nested tables.
type LLMOverrides struct {
	Provider        *string        `toml:"provider"`
	Model           *string        `toml:"model"`
	ProviderAPIKey  *string        `toml:"provider_api_key"`
	ProviderURL     *string        `toml:"provider_url"`
	MaxTokens       *int           `toml:"max_tokens"`
	ProviderOptions map[string]any `toml:"provider_options"`
}

// ResolvedLLM is a subagent's effective configuration with independent provider options.
type ResolvedLLM struct {
	Provider        string
	Model           string
	ProviderAPIKey  string
	ProviderURL     string
	ProviderOptions map[string]any
	MaxTokens       int
}

// LoadReviewPipeline strictly decodes TOML and validates every subagent, including
// optional ones. Relative prompt paths are resolved against the pipeline file's directory.
func LoadReviewPipeline(path string) (*ReviewPipeline, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("review pipeline path must not be empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve review pipeline path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read review pipeline %q: %w", path, err)
	}
	pipeline := &ReviewPipeline{MaxConcurrency: DefaultPipelineMaxConcurrency}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(pipeline); err != nil {
		return nil, fmt.Errorf("decode review pipeline %q: %w", path, err)
	}
	if pipeline.MaxConcurrency <= 0 {
		return nil, errors.New("review pipeline max_concurrency must be positive")
	}
	seen := make(map[string]bool, len(pipeline.Subagents))
	for i := range pipeline.Subagents {
		subagent := &pipeline.Subagents[i]
		subagent.Title = strings.TrimSpace(subagent.Title)
		if subagent.Title == "" {
			return nil, fmt.Errorf("subagent %d: title must not be empty", i+1)
		}
		key := strings.ToLower(subagent.Title)
		if seen[key] {
			return nil, fmt.Errorf("duplicate subagent title %q", subagent.Title)
		}
		seen[key] = true
		if err := subagent.loadPrompt(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("subagent %q: %w", subagent.Title, err)
		}
	}
	return pipeline, nil
}

func (s *Subagent) loadPrompt(directory string) error {
	s.Description = strings.TrimSpace(s.Description)
	if s.Description == "" {
		return errors.New("description must not be empty")
	}
	s.PromptFile = strings.TrimSpace(s.PromptFile)
	if s.PromptFile == "" {
		return errors.New("prompt_file must not be empty")
	}
	if s.LLM.MaxTokens != nil && *s.LLM.MaxTokens <= 0 {
		return errors.New("llm.max_tokens must be positive")
	}
	if !filepath.IsAbs(s.PromptFile) {
		s.PromptFile = filepath.Join(directory, s.PromptFile)
	}
	data, err := os.ReadFile(s.PromptFile)
	if err != nil {
		return fmt.Errorf("read prompt file %q: %w", s.PromptFile, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("prompt file %q must not be empty", s.PromptFile)
	}
	s.Prompt = string(data)
	return nil
}

// ResolveLLM inherits omitted fields from main and clones provider option maps and
// slices recursively, preserving concrete types without a lossy JSON round trip.
func (s Subagent) ResolveLLM(main *Config) ResolvedLLM {
	if main == nil {
		main = &Config{}
	}
	resolved := ResolvedLLM{
		Provider:        main.Provider,
		Model:           main.Model,
		ProviderAPIKey:  main.ProviderAPIKey,
		ProviderURL:     main.ProviderURL,
		ProviderOptions: cloneProviderOptions(main.ProviderOptions),
		MaxTokens:       main.MaxTokens,
	}
	for _, field := range []struct {
		override *string
		target   *string
	}{
		{s.LLM.Provider, &resolved.Provider},
		{s.LLM.Model, &resolved.Model},
		{s.LLM.ProviderAPIKey, &resolved.ProviderAPIKey},
		{s.LLM.ProviderURL, &resolved.ProviderURL},
	} {
		if field.override != nil {
			*field.target = *field.override
		}
	}
	if s.LLM.MaxTokens != nil {
		resolved.MaxTokens = *s.LLM.MaxTokens
	}
	if len(s.LLM.ProviderOptions) > 0 {
		if resolved.ProviderOptions == nil {
			resolved.ProviderOptions = make(map[string]any, len(s.LLM.ProviderOptions))
		}
		for key, value := range cloneProviderOptions(s.LLM.ProviderOptions) {
			resolved.ProviderOptions[key] = value
		}
	}
	return resolved
}

func cloneProviderOptions(options map[string]any) map[string]any {
	return cloneOptionValue(reflect.ValueOf(options)).Interface().(map[string]any)
}

// Provider options originate in JSON or TOML: recursively clone their map/slice
// containers, including typed slices, while leaving scalar types intact.
func cloneOptionValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneOptionValue(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		for iter := value.MapRange(); iter.Next(); {
			cloned.SetMapIndex(iter.Key(), cloneOptionValue(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneOptionValue(value.Index(i)))
		}
		return cloned
	default:
		return value
	}
}
