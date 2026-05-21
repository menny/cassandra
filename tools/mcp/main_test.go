package mcp

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Ignore known goroutine leak inside the third-party Model Context Protocol SDK server.go.
	// When the transport closes, the server loop can get blocked on channel sends/receives
	// inside its internal goroutines.
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/modelcontextprotocol/go-sdk/mcp.(*Server).Run.func1"),
	)
}
