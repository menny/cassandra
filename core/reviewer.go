package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/menny/cassandra/core/config"
	"github.com/menny/cassandra/core/prompts"
	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/factory"
	"github.com/menny/cassandra/tools"
	"github.com/menny/cassandra/tools/mcp"
)

// Reviewer encapsulates an initialized Agent and its environment.
type Reviewer struct {
	Agent                    *Agent
	Config                   *config.Config
	RootDir                  string
	StablePrompt             string
	Guidelines               string
	SupplementalGuidelines   []string
	ApprovalEvaluationPrompt string
	mcpManager               *mcp.Manager
}

// NewReviewer instantiates a Reviewer based on the provided configuration.
// targetDir is the directory where local tools (like grep) will operate.
func NewReviewer(ctx context.Context, cfg *config.Config, targetDir string) (r *Reviewer, err error) {
	client, err := factory.New(ctx, cfg.Provider, cfg.Model, cfg.ProviderAPIKey, cfg.ProviderURL)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize LLM: %w", err)
	}

	registry := tools.NewRegistry()
	tools.RegisterLocalTools(registry, targetDir, cfg.IgnoredLockFiles)

	var mcpManager *mcp.Manager
	// Ensure we close the MCP manager if we encounter an error later in this function.
	defer func() {
		if err != nil && mcpManager != nil {
			_ = mcpManager.Close()
		}
	}()

	var mcpConfig mcp.Config
	if cfg.MCPConfigFile != "" {
		mcpData, err := os.ReadFile(cfg.MCPConfigFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read MCP config file %s: %w", cfg.MCPConfigFile, err)
		}
		if err := json.Unmarshal(mcpData, &mcpConfig); err != nil {
			return nil, fmt.Errorf("failed to parse MCP config file %s: %w", cfg.MCPConfigFile, err)
		}
	}

	if cfg.AllowURLFetch {
		if mcpConfig.MCPServers == nil {
			mcpConfig.MCPServers = make(map[string]mcp.ServerConfig)
		}
		mcpConfig.MCPServers["mcp-server-fetch"] = mcp.ServerConfig{
			Command: "uvx",
			Args:    []string{"mcp-server-fetch"},
		}
	}

	if len(mcpConfig.MCPServers) > 0 {
		mcpConfig.ExpandEnv()
		mcpManager = mcp.NewManager()
		if err := mcpManager.RegisterServers(ctx, mcpConfig, func(def llm.ToolDef, handler func(context.Context, llm.ToolCall) (string, error)) {
			registry.RegisterTool(def, handler)
		}); err != nil {
			return nil, fmt.Errorf("failed to register MCP servers: %w", err)
		}
	}

	mainGuidelines, err := config.ResolveGuidelinesContent(cfg.MainGuidelines)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve main guidelines: %w", err)
	}

	var supplementalGuidelines []string
	for _, sg := range cfg.SupplementalGuidelines {
		content, err := config.ResolveGuidelinesContent(sg)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve supplemental guideline %q: %w", sg, err)
		}
		supplementalGuidelines = append(supplementalGuidelines, content)
	}

	var approvalEvaluationPrompt string
	if cfg.ApprovalEvaluationPromptFile != "" {
		content, err := os.ReadFile(cfg.ApprovalEvaluationPromptFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read approval evaluation prompt file: %w", err)
		}
		approvalEvaluationPrompt = string(content)
	}

	stable, _, _, err := prompts.BuildSystemPrompt(targetDir, nil, mainGuidelines, supplementalGuidelines, approvalEvaluationPrompt)
	if err != nil {
		return nil, fmt.Errorf("failed to build stable system prompt: %w", err)
	}

	return &Reviewer{
		Agent:                    NewAgent(client, registry),
		Config:                   cfg,
		RootDir:                  targetDir,
		StablePrompt:             stable,
		Guidelines:               mainGuidelines,
		SupplementalGuidelines:   supplementalGuidelines,
		ApprovalEvaluationPrompt: approvalEvaluationPrompt,
		mcpManager:               mcpManager,
	}, nil
}

// Close releases resources (like MCP server connections).
func (r *Reviewer) Close() error {
	if r.mcpManager != nil {
		return r.mcpManager.Close()
	}
	return nil
}

// Run executes a review for the given changes.
func (r *Reviewer) Run(ctx context.Context, changedFiles []string, requestText string) (string, error) {
	_, dynamic, _, err := prompts.BuildSystemPrompt(r.RootDir, changedFiles, r.Guidelines, r.SupplementalGuidelines, r.ApprovalEvaluationPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to build dynamic system prompt: %w", err)
	}

	maxIterations := CalculateMaxIterations(len(changedFiles))
	return r.Agent.RunReview(ctx, r.StablePrompt, dynamic, requestText, maxIterations, r.Config.MaxTokens)
}
