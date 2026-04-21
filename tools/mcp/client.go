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

func (m *Manager) RegisterServers(ctx context.Context, config Config, register func(llm.ToolDef, func(llm.ToolCall) (string, error))) error {
	for name, server := range config.MCPServers {
		if err := m.registerServer(ctx, name, server, register); err != nil {
			// Per output contract, we report errors to stderr but continue if possible
			fmt.Fprintf(os.Stderr, "Warning: failed to register MCP server %q: %v\n", name, err)
		}
	}
	return nil
}

type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

func (m *Manager) registerServer(ctx context.Context, serverName string, cfg ServerConfig, register func(llm.ToolDef, func(llm.ToolCall) (string, error))) error {
	var transport mcp.Transport

	if cfg.Command != "" {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if cfg.Env != nil {
			cmd.Env = os.Environ()
			for k, v := range cfg.Env {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
			}
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
		toolName := fmt.Sprintf("%s_%s", serverName, t.Name)

		// Map MCP JSON Schema to llm.ToolDef
		parameters := make(map[string]any)
		if t.InputSchema != nil {
			if m, ok := t.InputSchema.(map[string]any); ok {
				parameters = m
			} else {
				// Fallback to unmarshaling if it's not a map
				data, err := json.Marshal(t.InputSchema)
				if err == nil {
					_ = json.Unmarshal(data, &parameters)
				}
			}
		}

		def := llm.ToolDef{
			Name:        toolName,
			Description: fmt.Sprintf("[%s] %s", serverName, t.Description),
			Parameters:  parameters,
		}

		handler := func(tc llm.ToolCall) (string, error) {
			var args map[string]any
			if err := tc.UnmarshalArguments(&args); err != nil {
				return "", err
			}

			callParams := &mcp.CallToolParams{
				Name:      t.Name,
				Arguments: args,
			}

			res, err := session.CallTool(ctx, callParams)
			if err != nil {
				return "", fmt.Errorf("MCP tool call failed: %w", err)
			}

			if res.IsError {
				return "", fmt.Errorf("MCP tool returned error")
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
			return result.String(), nil
		}

		register(def, handler)
	}

	return nil
}
