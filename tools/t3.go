package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/menny/cassandra/llm"
)

// IsT3Session returns true if Cassandra is currently running inside a T3 Code environment.
func IsT3Session() bool {
	return os.Getenv("T3_ACP_MCP_ENDPOINT") != "" || os.Getenv("T3_ACP_MCP_NODE") != ""
}

func callT3McpTool(tool string, args any) (string, error) {
	if !IsT3Session() {
		return "", fmt.Errorf("tool %q is only available in a T3 Code session", tool)
	}

	node := os.Getenv("T3_ACP_MCP_NODE")
	if node == "" {
		return "", fmt.Errorf("T3 MCP node executable (T3_ACP_MCP_NODE) is not configured")
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("failed to serialize args for %q: %w", tool, err)
	}

	var cmdArgs []string
	if entrypoint := os.Getenv("T3_ACP_MCP_ENTRYPOINT"); entrypoint != "" {
		cmdArgs = append(cmdArgs, entrypoint)
	}
	cmdArgs = append(cmdArgs, "acp-mcp-call", tool, string(argsJSON))
	cmd := exec.Command(node, cmdArgs...)
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("calling T3 MCP tool %q failed: %w (output: %s)", tool, err, string(out))
	}
	return string(out), nil
}

// ModelSelection specifies provider instance and model parameters for thread launch.
type ModelSelection struct {
	InstanceID string `json:"instanceId,omitempty"`
	Model      string `json:"model"`
	Options    any    `json:"options,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

// WorkspaceStrategy specifies workspace configuration for a launched thread.
type WorkspaceStrategy struct {
	Type            string `json:"type"`
	BaseRef         string `json:"baseRef,omitempty"`
	Branch          string `json:"branch,omitempty"`
	StartFromOrigin *bool  `json:"startFromOrigin,omitempty"`
	WorktreePath    string `json:"worktreePath,omitempty"`
}

// ThreadLaunchArgs represents arguments passed to t3_thread_launch.
type ThreadLaunchArgs struct {
	Title             string             `json:"title"`
	Message           string             `json:"message,omitempty"`
	ModelSelection    *ModelSelection    `json:"modelSelection,omitempty"`
	WorkspaceStrategy *WorkspaceStrategy `json:"workspaceStrategy,omitempty"`
	RuntimeMode       string             `json:"runtimeMode,omitempty"`
	InteractionMode   string             `json:"interactionMode,omitempty"`
	ProjectID         string             `json:"projectId,omitempty"`
	Scratch           *bool              `json:"scratch,omitempty"`
}

func registerT3Tools(r *Registry) {
	// Only expose T3-specific tools when running inside a T3 Code environment.
	if !IsT3Session() {
		return
	}

	linkDef := llm.ToolDef{
		Name:        "link_pull_request",
		Description: "Register every pull request you open for this thread, including each layer of a stack, right after creating it. Links a pull request to this thread so T3 Code tracks it, shows its status beside the thread, and settles the thread when it merges. Pass the URL, or repository plus number. Linking an already-linked pull request succeeds with alreadyLinked=true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Full pull request URL.",
				},
				"repository": map[string]any{
					"type":        "string",
					"description": "Owner and repository name.",
				},
				"number": map[string]any{
					"type":        "integer",
					"description": "Pull request number.",
				},
			},
		},
	}

	type linkArgs struct {
		URL        string `json:"url"`
		Repository string `json:"repository"`
		Number     int    `json:"number"`
	}

	RegisterToolWithArgs(r, linkDef, func(ctx context.Context, args linkArgs) (string, error) {
		return callT3McpTool("link_pull_request", args)
	})

	listDef := llm.ToolDef{
		Name:        "list_thread_pull_requests",
		Description: "List the pull requests linked to a thread with their last known host state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"threadId": map[string]any{
					"type":        "string",
					"description": "Thread ID. Omit for the current thread.",
				},
			},
		},
	}

	type listArgs struct {
		ThreadID string `json:"threadId"`
	}

	RegisterToolWithArgs(r, listDef, func(ctx context.Context, args listArgs) (string, error) {
		return callT3McpTool("list_thread_pull_requests", args)
	})

	capDef := llm.ToolDef{
		Name:        "orchestrator_capabilities",
		Description: "List the V2 provider instances and their current models from the same live catalog as the composer, including configured custom models, inherited runtime settings, and app-owned orchestration features available to this caller.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}

	type capArgs struct{}

	RegisterToolWithArgs(r, capDef, func(ctx context.Context, args capArgs) (string, error) {
		return callT3McpTool("orchestrator_capabilities", args)
	})

	launchDef := llm.ToolDef{
		Name:        "t3_thread_launch",
		Description: "Create a new top-level thread in T3 Code to carry out tasks, such as addressing code review findings. Use this tool when explicitly asked by the user, or after the user agrees to spawn a fix session.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Title of the thread, formatted as 'Review Fix [short description]'.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Initial task instructions for the spawned thread, including context, user directions, and technical fix guidelines.",
				},
				"modelSelection": map[string]any{
					"type":        "object",
					"description": "Model and provider selection. Use orchestrator_capabilities to pick instanceId and model.",
					"properties": map[string]any{
						"instanceId": map[string]any{
							"type":        "string",
							"description": "The provider instance ID (e.g., from orchestrator_capabilities).",
						},
						"model": map[string]any{
							"type":        "string",
							"description": "The model ID selected from that provider instance's available models.",
						},
					},
					"required": []string{"model"},
				},
				"workspaceStrategy": map[string]any{
					"type":        "object",
					"description": "Optional workspace strategy (e.g. worktree, existing_worktree, root). Defaults to project root.",
				},
			},
			"required": []string{"title", "message"},
		},
	}

	RegisterToolWithArgs(r, launchDef, func(ctx context.Context, args ThreadLaunchArgs) (string, error) {
		trimmed := strings.TrimSpace(args.Title)
		if strings.HasPrefix(trimmed, "Review Fix") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Review Fix"))
			rest = strings.TrimPrefix(rest, "-")
			rest = strings.TrimPrefix(rest, ":")
			rest = strings.TrimSpace(rest)
			if rest != "" {
				args.Title = "Review Fix " + rest
			} else {
				args.Title = "Review Fix"
			}
		} else if trimmed != "" {
			args.Title = "Review Fix " + trimmed
		} else {
			args.Title = "Review Fix"
		}
		return callT3McpTool("t3_thread_launch", args)
	})
}
