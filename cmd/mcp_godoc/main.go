package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/menny/cassandra/tools/mcp_servers/godoc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := godoc.NewServer()

	// Use stdio transport for local MCP communication.
	transport := &mcp.StdioTransport{}

	fmt.Fprintf(os.Stderr, "Starting godoc MCP server on stdio...\n")
	return server.Serve(ctx, transport)
}
