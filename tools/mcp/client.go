package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/menny/cassandra/llm"
)

type Manager struct {
	sessions []*mcp.ClientSession
	mu       sync.Mutex
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for _, session := range m.sessions {
		if err := session.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	m.sessions = nil
	if len(errs) > 0 {
		return fmt.Errorf("failed to close some sessions: %s", strings.Join(errs, ", "))
	}
	return nil
}

func (m *Manager) RegisterServers(ctx context.Context, config Config, register func(llm.ToolDef, func(context.Context, llm.ToolCall) (string, error))) error {
	var lastErr error
	var successCount int
	for name, server := range config.MCPServers {
		if err := m.registerServer(ctx, name, server, register); err != nil {
			// Per output contract, we report errors to stderr but continue if possible
			fmt.Fprintf(os.Stderr, "Warning: failed to register MCP server %q: %v\n", name, err)
			lastErr = err
		} else {
			successCount++
		}
	}

	if len(config.MCPServers) > 0 && successCount == 0 {
		return fmt.Errorf("none of the %d configured MCP servers could be registered (last error: %w)", len(config.MCPServers), lastErr)
	}

	return nil
}

type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// The RoundTripper contract prohibits modifying the input request.
	// We must clone the request before adding headers.
	out := req.Clone(req.Context())
	for k, v := range h.headers {
		out.Header.Set(k, v)
	}
	return h.base.RoundTrip(out)
}

func (m *Manager) registerServer(ctx context.Context, serverName string, cfg ServerConfig, register func(llm.ToolDef, func(context.Context, llm.ToolCall) (string, error))) error {
	var transport mcp.Transport

	if cfg.Command != "" {
		// Use CommandContext to ensure that the subprocess is reaped if the
		// main application context is canceled.
		cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)

		// Ensure the command runs in the workspace root or the process's current CWD.
		if cwd, err := os.Getwd(); err == nil {
			cmd.Dir = cwd
		}

		// Security: Prevent secret leakage by not inheriting the full host environment.
		// However, we MUST inherit essential system variables for the command to execute.
		essential := []string{"PATH", "HOME", "USER", "TMPDIR"}
		for _, e := range essential {
			if val, ok := os.LookupEnv(e); ok {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", e, val))
			}
		}

		// Add explicitly configured environment variables.
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}

		transport = &mcp.CommandTransport{
			Command: cmd,
		}
	} else if cfg.URL != "" {
		// SSE transport
		sseTransport := &mcp.SSEClientTransport{
			Endpoint: cfg.URL,
		}
		if len(cfg.Headers) > 0 {
			sseTransport.HTTPClient = &http.Client{
				Transport: &headerRoundTripper{
					headers: cfg.Headers,
					base:    http.DefaultTransport,
				},
			}
		}
		transport = sseTransport
	} else {
		return fmt.Errorf("invalid server config: neither command nor url specified")
	}

	return m.registerServerWithTransport(ctx, serverName, transport, cfg, register)
}

func (m *Manager) registerServerWithTransport(ctx context.Context, serverName string, transport mcp.Transport, cfg ServerConfig, register func(llm.ToolDef, func(context.Context, llm.ToolCall) (string, error))) error {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "cassandra-reviewer",
		Version: "0.0.1",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	m.mu.Lock()
	m.sessions = append(m.sessions, session)
	m.mu.Unlock()

	toolsRes, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	for _, t := range toolsRes.Tools {
		t := t // capture range variable
		toolName := fmt.Sprintf("%s_%s", serverName, t.Name)

		// Map MCP JSON Schema to llm.ToolDef
		parameters := make(map[string]any)
		if t.InputSchema != nil {
			if m, ok := t.InputSchema.(map[string]any); ok {
				parameters = m
			} else {
				// Fallback to unmarshaling if it's not a map
				data, err := json.Marshal(t.InputSchema)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to marshal input schema for tool %q: %v. Skipping tool registration.\n", t.Name, err)
					continue
				}
				if err := json.Unmarshal(data, &parameters); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to unmarshal input schema for tool %q: %v. Skipping tool registration.\n", t.Name, err)
					continue
				}
			}
		}

		def := llm.ToolDef{
			Name:        toolName,
			Description: fmt.Sprintf("[%s] %s", serverName, t.Description),
			Parameters:  parameters,
		}

		handler := func(ctx context.Context, tc llm.ToolCall) (string, error) {
			// We use map[string]any here as an intentional exception to the
			// project's struct-based argument guideline because MCP tool
			// schemas are dynamic and discovered only at runtime.
			var args map[string]any
			if err := tc.UnmarshalArguments(&args); err != nil {
				return "", err
			}

			// Per review feedback: ensure we don't block the agent indefinitely
			// and respect application-level shutdown signals by chaining from ctx.
			timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
			defer cancel()

			callParams := &mcp.CallToolParams{
				Name:      t.Name,
				Arguments: args,
			}

			res, err := session.CallTool(timeoutCtx, callParams)
			if err != nil {
				return "", fmt.Errorf("MCP tool call failed: %w", err)
			}

			var result strings.Builder
			for _, content := range res.Content {
				switch c := content.(type) {
				case *mcp.TextContent:
					result.WriteString(c.Text)
				case *mcp.ImageContent:
					result.WriteString("[Image Content]")
				case *mcp.EmbeddedResource:
					result.WriteString("[Resource Content]")
				}
			}

			if res.IsError {
				// Return the tool's error content to the model so it can potentially self-correct.
				return "", fmt.Errorf("MCP tool error: %s", result.String())
			}

			return result.String(), nil
		}

		register(def, handler)
	}

	return nil
}
