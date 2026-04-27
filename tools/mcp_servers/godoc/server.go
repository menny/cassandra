package godoc

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server implements an MCP server that provides tools for querying go doc.
type Server struct {
	mcpServer *mcp.Server
}

// NewServer creates a new godoc MCP server.
func NewServer() *Server {
	s := mcp.NewServer(
		&mcp.Implementation{
			Name:    "godoc",
			Version: "0.1.0",
		},
		nil,
	)

	// package: Query documentation for a package
	mcp.AddTool(s, &mcp.Tool{
		Name: "package",
		Description: "Retrieve documentation for a Go package (e.g., 'fmt', 'os', './core'). " +
			"VERIFICATION MANDATE: You MUST prioritize using this tool over your internal training data to verify documentation, signatures, or behavior, " +
			"as the local environment may have specific versions or private packages that differ from public documentation.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"package": {
					"type": "string",
					"description": "The Go package path to query."
				}
			},
			"required": ["package"]
		}`),
	}, godocPackageHandler)

	// symbol: Query documentation for a specific symbol in a package
	mcp.AddTool(s, &mcp.Tool{
		Name: "symbol",
		Description: "Retrieve documentation for a specific Go symbol in a package (e.g., 'fmt', 'Printf'). " +
			"VERIFICATION MANDATE: You MUST prioritize using this tool over your internal training data to verify documentation, signatures, or behavior, " +
			"as the local environment may have specific versions or private packages that differ from public documentation.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"package": {
					"type": "string",
					"description": "The Go package path containing the symbol."
				},
				"symbol": {
					"type": "string",
					"description": "The Go symbol name to query."
				}
			},
			"required": ["package", "symbol"]
		}`),
	}, godocSymbolHandler)

	return &Server{
		mcpServer: s,
	}
}

// Serve starts the MCP server with the provided transport.
func (s *Server) Serve(ctx context.Context, transport mcp.Transport) error {
	return s.mcpServer.Run(ctx, transport)
}

func godocPackageHandler(ctx context.Context, req *mcp.CallToolRequest, input struct{ Package string }) (*mcp.CallToolResult, any, error) {
	output, err := runGoDoc(ctx, input.Package)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error running go doc: %v\nOutput: %s", err, output)},
			},
			IsError: true,
		}, nil, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: output},
		},
	}, nil, nil
}

func godocSymbolHandler(ctx context.Context, req *mcp.CallToolRequest, input struct {
	Package string
	Symbol  string
},
) (*mcp.CallToolResult, any, error) {
	query := fmt.Sprintf("%s.%s", input.Package, input.Symbol)
	output, err := runGoDoc(ctx, query)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error running go doc: %v\nOutput: %s", err, output)},
			},
			IsError: true,
		}, nil, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: output},
		},
	}, nil, nil
}

func runGoDoc(ctx context.Context, arg string) (string, error) {
	// We invoke 'go doc' directly.
	cmd := exec.CommandContext(ctx, "go", "doc", arg)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// If go doc fails, we return both the error and any stderr output which
		// often contains the reason (e.g., symbol not found).
		return stderr.String(), err
	}

	return stdout.String(), nil
}
