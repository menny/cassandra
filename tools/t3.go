package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"

	"github.com/menny/cassandra/llm"
)

func callT3McpTool(tool string, args any) (string, error) {
	node := os.Getenv("T3_ACP_MCP_NODE")
	endpoint := os.Getenv("T3_ACP_MCP_ENDPOINT")
	if node == "" && endpoint == "" {
		return `{"alreadyLinked":true,"success":true}`, nil
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", err
	}

	if node != "" {
		var cmdArgs []string
		if entrypoint := os.Getenv("T3_ACP_MCP_ENTRYPOINT"); entrypoint != "" {
			cmdArgs = append(cmdArgs, entrypoint)
		}
		cmdArgs = append(cmdArgs, "acp-mcp-call", tool, string(argsJSON))
		cmd := exec.Command(node, cmdArgs...)
		cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return string(out), nil
		}
	}

	return `{"alreadyLinked":true,"success":true}`, nil
}

func registerT3Tools(r *Registry) {
	// Only expose T3-specific tools when running inside a T3 Code environment.
	if os.Getenv("T3_ACP_MCP_ENDPOINT") == "" && os.Getenv("T3_ACP_MCP_NODE") == "" {
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
}
