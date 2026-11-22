package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const validSubagent = `
[[subagents]]
title = "Security"
description = "Find security issues"
prompt_file = "prompt.md"
`

func writePipelineFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReviewPipelineDefaultsAndPaths(t *testing.T) {
	dir := t.TempDir()
	prompt := "  Inspect the changes.\n"
	writePipelineFile(t, filepath.Join(dir, "prompt.md"), prompt)
	path := filepath.Join(dir, "pipeline.toml")
	writePipelineFile(t, path, `
[[subagents]]
title = "  Security  "
description = "  Find issues  "
prompt_file = "  prompt.md  "
`)
	pipeline, err := LoadReviewPipeline(" " + path + " ")
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.MaxConcurrency != 4 || len(pipeline.Subagents) != 1 {
		t.Fatalf("unexpected pipeline: %+v", pipeline)
	}
	s := pipeline.Subagents[0]
	if s.Title != "Security" || s.Description != "Find issues" || s.PromptFile != filepath.Join(dir, "prompt.md") || s.Prompt != prompt {
		t.Errorf("unexpected subagent: %+v", s)
	}
	if s.Required || s.UserVisible || s.ReviewerInput || s.AllowMCPs {
		t.Errorf("flags must default to false: %+v", s)
	}
	if s.LLM.Provider != nil || s.LLM.Model != nil || s.LLM.ProviderAPIKey != nil || s.LLM.ProviderURL != nil || s.LLM.MaxTokens != nil {
		t.Errorf("omitted overrides must remain nil: %+v", s.LLM)
	}

	t.Run("absolute prompt path", func(t *testing.T) {
		writePipelineFile(t, path, fmt.Sprintf(`max_concurrency = 2
[[subagents]]
title = "Absolute"
description = "Absolute path"
prompt_file = %q
required = true
user_visible = true
reviewer_input = true
`, filepath.Join(dir, "prompt.md")))
		pipeline, err := LoadReviewPipeline(path)
		if err != nil {
			t.Fatal(err)
		}
		s := pipeline.Subagents[0]
		if pipeline.MaxConcurrency != 2 || !s.Required || !s.UserVisible || !s.ReviewerInput || s.PromptFile != filepath.Join(dir, "prompt.md") {
			t.Fatalf("unexpected explicit values: %+v, %+v", pipeline, s)
		}
	})

	t.Run("relative pipeline path", func(t *testing.T) {
		// Resolve both pipeline and prompt relative to the actual working directory.
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(cwd, path)
		if err != nil {
			t.Fatal(err)
		}
		writePipelineFile(t, path, validSubagent)
		pipeline, err := LoadReviewPipeline(relative)
		if err != nil {
			t.Fatal(err)
		}
		if pipeline.Subagents[0].PromptFile != filepath.Join(dir, "prompt.md") {
			t.Fatalf("unexpected resolved path: %q", pipeline.Subagents[0].PromptFile)
		}
	})
}

func TestLoadReviewPipelineAllowMCPs(t *testing.T) {
	dir := t.TempDir()
	writePipelineFile(t, filepath.Join(dir, "prompt.md"), "Review")
	path := filepath.Join(dir, "pipeline.toml")
	writePipelineFile(t, path, validSubagent+`allow_mcps = true

[[subagents]]
title = "Default MCP access"
description = "MCP tools remain disabled when omitted"
prompt_file = "prompt.md"

[[subagents]]
title = "Explicitly disabled MCP access"
description = "MCP tools can be explicitly disabled"
prompt_file = "prompt.md"
allow_mcps = false

[[subagents]]
title = "Independent MCP access"
description = "MCP tools can be enabled independently"
prompt_file = "prompt.md"
allow_mcps = true
`)
	pipeline, err := LoadReviewPipeline(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, true}
	if len(pipeline.Subagents) != len(want) {
		t.Fatalf("got %d subagents; want %d", len(pipeline.Subagents), len(want))
	}
	for i, subagent := range pipeline.Subagents {
		if subagent.AllowMCPs != want[i] {
			t.Errorf("subagent %q AllowMCPs = %v; want %v", subagent.Title, subagent.AllowMCPs, want[i])
		}
	}
}

func TestLoadReviewPipelineInvalid(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"syntax", "[[subagents]", "decode review pipeline"},
		{"unknown root field", "concurrency = 4", "decode review pipeline"},
		{"unknown subagent field", validSubagent + "typo = true\n", "decode review pipeline"},
		{"unknown llm field", validSubagent + "[subagents.llm]\nmodle = 'test'\n", "decode review pipeline"},
		{"prompt field is excluded", validSubagent + "prompt = 'injected'\n", "decode review pipeline"},
		{"wrong concurrency type", "max_concurrency = '4'", "decode review pipeline"},
		{"fractional concurrency", "max_concurrency = 1.5", "decode review pipeline"},
		{"zero concurrency", "max_concurrency = 0", "max_concurrency must be positive"},
		{"negative concurrency", "max_concurrency = -1", "max_concurrency must be positive"},
		{"missing title", strings.ReplaceAll(validSubagent, `title = "Security"`, ""), "title must not be empty"},
		{"blank title", strings.ReplaceAll(validSubagent, `title = "Security"`, `title = "  "`), "title must not be empty"},
		{"duplicate title", validSubagent + strings.ReplaceAll(validSubagent, `"Security"`, `" security "`), "duplicate subagent title"},
		{"missing description", strings.ReplaceAll(validSubagent, `description = "Find security issues"`, ""), "description must not be empty"},
		{"blank description", strings.ReplaceAll(validSubagent, `description = "Find security issues"`, `description = "  "`), "description must not be empty"},
		{"missing prompt_file", strings.ReplaceAll(validSubagent, `prompt_file = "prompt.md"`, ""), "prompt_file must not be empty"},
		{"blank prompt_file", strings.ReplaceAll(validSubagent, `prompt_file = "prompt.md"`, `prompt_file = "  "`), "prompt_file must not be empty"},
		{"missing prompt file", strings.ReplaceAll(validSubagent, `"prompt.md"`, `"missing.md"`), "read prompt file"},
		{"empty optional prompt", strings.ReplaceAll(validSubagent, `"prompt.md"`, `"empty.md"`), "must not be empty"},
		{"whitespace optional prompt", strings.ReplaceAll(validSubagent, `"prompt.md"`, `"blank.md"`), "must not be empty"},
		{"zero max_tokens", validSubagent + "[subagents.llm]\nmax_tokens = 0\n", "llm.max_tokens must be positive"},
		{"negative max_tokens", validSubagent + "[subagents.llm]\nmax_tokens = -5\n", "llm.max_tokens must be positive"},
		{"wrong flag type", validSubagent + "required = 'true'\n", "decode review pipeline"},
		{"wrong MCP flag type", validSubagent + "allow_mcps = 'true'\n", "decode review pipeline"},
		{"unknown MCP flag", validSubagent + "allow_mcp = true\n", "decode review pipeline"},
		{"MCP flag at root", "allow_mcps = true\n" + validSubagent, "decode review pipeline"},
		{"MCP flag in llm", validSubagent + "[subagents.llm]\nallow_mcps = true\n", "decode review pipeline"},
		{"invalid second optional agent", validSubagent + strings.ReplaceAll(strings.ReplaceAll(validSubagent, `"Security"`, `"Other"`), `"prompt.md"`, `"missing.md"`), "read prompt file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writePipelineFile(t, filepath.Join(dir, "prompt.md"), "Check code")
			writePipelineFile(t, filepath.Join(dir, "empty.md"), "")
			writePipelineFile(t, filepath.Join(dir, "blank.md"), " \n\t")
			path := filepath.Join(dir, "pipeline.toml")
			writePipelineFile(t, path, tt.content)
			pipeline, err := LoadReviewPipeline(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadReviewPipeline() = %+v, %v; want error containing %q", pipeline, err, tt.wantErr)
			}
			if pipeline != nil {
				t.Error("invalid pipeline must not be returned")
			}
		})
	}
}

func TestLoadReviewPipelineFileErrors(t *testing.T) {
	if _, err := LoadReviewPipeline("  "); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := LoadReviewPipeline(filepath.Join(t.TempDir(), "missing.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected wrapped missing file error, got %v", err)
	}
}

func TestLoadReviewPipelineOverrides(t *testing.T) {
	dir := t.TempDir()
	writePipelineFile(t, filepath.Join(dir, "prompt.md"), "Review")
	path := filepath.Join(dir, "pipeline.toml")
	writePipelineFile(t, path, validSubagent+`
[subagents.llm]
provider = "sub-provider"
model = "sub-model"
provider_api_key = ""
provider_url = ""
max_tokens = 512
[subagents.llm.provider_options]
custom = 9223372036854775807
[subagents.llm.provider_options.nested]
replacement = true
`)
	pipeline, err := LoadReviewPipeline(path)
	if err != nil {
		t.Fatal(err)
	}
	main := &Config{
		Provider: "main-provider", Model: "main-model", ProviderAPIKey: "secret", ProviderURL: "https://provider", MaxTokens: 1024,
		ProviderOptions: map[string]any{"inherited": true, "nested": map[string]any{"original": true}},
	}
	resolved := pipeline.Subagents[0].ResolveLLM(main)
	if resolved.Provider != "sub-provider" || resolved.Model != "sub-model" || resolved.ProviderAPIKey != "" || resolved.ProviderURL != "" || resolved.MaxTokens != 512 {
		t.Fatalf("unexpected resolved overrides: %+v", resolved)
	}
	wantOptions := map[string]any{"inherited": true, "custom": int64(9223372036854775807), "nested": map[string]any{"replacement": true}}
	if !reflect.DeepEqual(resolved.ProviderOptions, wantOptions) {
		t.Errorf("provider options = %#v; want %#v", resolved.ProviderOptions, wantOptions)
	}
	resolved.ProviderOptions["nested"].(map[string]any)["replacement"] = false
	if pipeline.Subagents[0].LLM.ProviderOptions["nested"].(map[string]any)["replacement"] != true {
		t.Error("resolved provider options alias the subagent")
	}

	writePipelineFile(t, path, validSubagent+`[subagents.llm]
provider = ""
model = ""
`)
	pipeline, err = LoadReviewPipeline(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved = pipeline.Subagents[0].ResolveLLM(main)
	if resolved.Provider != "" || resolved.Model != "" || resolved.ProviderAPIKey != main.ProviderAPIKey || resolved.ProviderURL != main.ProviderURL || resolved.MaxTokens != main.MaxTokens {
		t.Fatalf("empty overrides or inheritance not respected: %+v", resolved)
	}
}

func TestResolveLLMInheritanceAndIsolation(t *testing.T) {
	main := &Config{
		Provider: "provider", Model: "model", ProviderAPIKey: "key", ProviderURL: "url", MaxTokens: 123,
		ProviderOptions: map[string]any{
			"nested": map[string]any{"slice": []any{map[string]any{"large": int64(9223372036854775807)}}},
			"typed":  []map[string][]string{{"values": {"original"}}},
			"nil":    nil,
			"nilmap": map[string]any(nil),
		},
	}
	s := Subagent{}
	resolved := s.ResolveLLM(main)
	want := ResolvedLLM{
		Provider: main.Provider, Model: main.Model, ProviderAPIKey: main.ProviderAPIKey, ProviderURL: main.ProviderURL,
		MaxTokens: main.MaxTokens, ProviderOptions: main.ProviderOptions,
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("inherited config = %+v; want %+v", resolved, want)
	}
	resolved.ProviderOptions["nested"].(map[string]any)["slice"].([]any)[0].(map[string]any)["large"] = int64(0)
	resolved.ProviderOptions["typed"].([]map[string][]string)[0]["values"][0] = "modified"
	resolved.ProviderOptions["new"] = true
	if again := s.ResolveLLM(main); !reflect.DeepEqual(again, want) {
		t.Fatalf("resolution mutated main: %+v", again)
	}
	t.Run("nil main config", func(t *testing.T) {
		provider, model := "google", "gemini-test"
		sub := Subagent{LLM: LLMOverrides{Provider: &provider, Model: &model}}
		got := sub.ResolveLLM(nil)
		if got.Provider != "google" || got.Model != "gemini-test" || got.MaxTokens != 0 {
			t.Fatalf("unexpected resolved config for nil main: %+v", got)
		}
	})
	t.Run("nil options", func(t *testing.T) {
		if got := s.ResolveLLM(&Config{}); got.ProviderOptions != nil {
			t.Errorf("nil options changed: %#v", got.ProviderOptions)
		}
	})
	t.Run("override without inherited options", func(t *testing.T) {
		sub := Subagent{LLM: LLMOverrides{ProviderOptions: map[string]any{"slice": []string{"original"}}}}
		got := sub.ResolveLLM(&Config{})
		got.ProviderOptions["slice"].([]string)[0] = "modified"
		if sub.LLM.ProviderOptions["slice"].([]string)[0] != "original" {
			t.Fatal("override options share slices with resolved options")
		}
	})
}

func TestLoadPipelineConfigAndRejectLegacy(t *testing.T) {
	for _, key := range []string{"pre-review-prompt", "pre-review-model"} {
		for _, value := range []string{"", "legacy"} {
			t.Run(key+"="+value, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "cassandra.toml")
				writePipelineFile(t, path, fmt.Sprintf("%s = %q\n", key, value))
				if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "review-pipeline-file") {
					t.Fatalf("expected migration error, got %v", err)
				}
			})
		}
	}
	path := filepath.Join(t.TempDir(), "cassandra.toml")
	writePipelineFile(t, path, "review-pipeline-file = 'pipeline.toml'\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReviewPipelineFile != "pipeline.toml" {
		t.Errorf("ReviewPipelineFile = %q", cfg.ReviewPipelineFile)
	}
	if NewDefaultConfig().ReviewPipelineFile != "" {
		t.Error("pipeline must be opt-in")
	}
}

func TestRejectLegacyPreReviewFlags(t *testing.T) {
	for _, key := range []string{"pre-review-prompt", "pre-review-model"} {
		for _, args := range [][]string{nil, {"--" + key + "="}, {"--" + key + "=legacy"}} {
			t.Run(fmt.Sprintf("%s/%v", key, args), func(t *testing.T) {
				flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
				flags.String(key, "", "legacy flag")
				if err := flags.Parse(args); err != nil {
					t.Fatal(err)
				}
				v := viper.New()
				if err := v.BindPFlags(flags); err != nil {
					t.Fatal(err)
				}
				err := RejectLegacyPreReview(v)
				if (err != nil) != (len(args) > 0) {
					t.Fatalf("RejectLegacyPreReview() = %v for args %v", err, args)
				}
			})
		}
	}
}
