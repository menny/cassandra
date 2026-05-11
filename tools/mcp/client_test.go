package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/menny/cassandra/llm"
)

func TestManager_RegisterServers_Mock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager()
	defer manager.Close()

	// Setup in-memory transports
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	// Start a mock MCP server in the background
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)

	// Add a simple tool to the mock server
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echoes input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{ Msg string }) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "echo: " + input.Msg},
			},
		}, nil, nil
	})

	// Add an error tool to the mock server
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail",
		Description: "always fails",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest, input any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "error details"},
			},
			IsError: true,
		}, nil, nil
	})

	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	var registeredTools []llm.ToolDef
	handlers := make(map[string]func(context.Context, llm.ToolCall) (string, error))

	register := func(def llm.ToolDef, handler func(context.Context, llm.ToolCall) (string, error)) {
		registeredTools = append(registeredTools, def)
		handlers[def.Name] = handler
	}

	cfg := ServerConfig{TimeoutSeconds: 30}
	err := manager.registerServerWithTransport(ctx, "myserver", clientTransport, cfg, func(string, string, error) {}, register)
	require.NoError(t, err)

	assert.Len(t, registeredTools, 2)

	// Test calling the echo tool
	result, err := handlers["myserver_echo"](ctx, llm.ToolCall{
		Name:      "myserver_echo",
		Arguments: `{"msg":"hello"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, "echo: hello", result)

	// Test calling the fail tool (should return error)
	_, err = handlers["myserver_fail"](ctx, llm.ToolCall{
		Name:      "myserver_fail",
		Arguments: `{}`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "MCP tool error: error details")
}

func TestManager_RegisterServers_Reporting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager()
	defer manager.Close()

	// 1. Success case
	serverTransport, _ := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	go func() { _ = server.Run(ctx, serverTransport) }()

	// We'll mock registerServer by temporarily overriding the transport creation logic if it were possible,
	// but since RegisterServers uses registerServer which creates transports based on config,
	// we'll just test the reporting flow by providing a config that will fail and one that will (hypothetically) succeed if we could mock the transport.
	// Actually, let's just test that it reports "started" and then "failed to load" for an invalid config.

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"invalid": {
				Command: "",
				URL:     "",
			},
		},
	}

	var reports []struct {
		name   string
		status string
		err    error
	}
	report := func(name, status string, err error) {
		reports = append(reports, struct {
			name   string
			status string
			err    error
		}{name, status, err})
	}

	err := manager.RegisterServers(ctx, cfg, report, func(llm.ToolDef, func(context.Context, llm.ToolCall) (string, error)) {})
	assert.Error(t, err)
	assert.Len(t, reports, 2)
	assert.Equal(t, "invalid", reports[0].name)
	assert.Equal(t, "started", reports[0].status)
	assert.Equal(t, "invalid", reports[1].name)
	assert.Equal(t, "failed to load", reports[1].status)
	assert.Error(t, reports[1].err)
	assert.Contains(t, reports[1].err.Error(), "invalid server config")
}
