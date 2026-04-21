package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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

	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	manager.mu.Lock()
	manager.sessions = append(manager.sessions, session)
	manager.mu.Unlock()

	var registeredTools []llm.ToolDef
	handlers := make(map[string]func(llm.ToolCall) (string, error))

	register := func(def llm.ToolDef, handler func(llm.ToolCall) (string, error)) {
		registeredTools = append(registeredTools, def)
		handlers[def.Name] = handler
	}

	// We've manually established the session, now we simulate the tool registration logic
	// that Manager.registerServer normally performs after connection.
	toolsRes, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	serverName := "myserver"
	for _, t := range toolsRes.Tools {
		toolName := serverName + "_" + t.Name

		parameters := make(map[string]any)
		if t.InputSchema != nil {
			data, _ := json.Marshal(t.InputSchema)
			_ = json.Unmarshal(data, &parameters)
		}

		def := llm.ToolDef{
			Name:        toolName,
			Description: fmt.Sprintf("[%s] %s", serverName, t.Description),
			Parameters:  parameters,
		}

		handler := func(tc llm.ToolCall) (string, error) {
			var args map[string]any
			_ = tc.UnmarshalArguments(&args)
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: t.Name, Arguments: args})
			if err != nil {
				return "", err
			}
			return res.Content[0].(*mcp.TextContent).Text, nil
		}
		register(def, handler)
	}

	assert.Len(t, registeredTools, 1)
	assert.Equal(t, "myserver_echo", registeredTools[0].Name)

	// Test calling the tool
	result, err := handlers["myserver_echo"](llm.ToolCall{
		Name:      "myserver_echo",
		Arguments: `{"msg":"hello"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, "echo: hello", result)
}
