package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpandEnv(t *testing.T) {
	t.Setenv("TEST_CMD", "my-cmd")
	t.Setenv("TEST_ARG", "my-arg")
	t.Setenv("TEST_ENV_VAL", "my-env-val")
	t.Setenv("TEST_URL", "http://example.com")
	t.Setenv("TEST_HEADER", "Bearer my-token")

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"server1": {
				Command: "${TEST_CMD}",
				Args:    []string{"${TEST_ARG}"},
				Env: map[string]string{
					"VAR": "${TEST_ENV_VAL}",
				},
				URL: "${TEST_URL}",
				Headers: map[string]string{
					"Authorization": "${TEST_HEADER}",
				},
			},
		},
	}

	cfg.ExpandEnv()

	server := cfg.MCPServers["server1"]
	assert.Equal(t, "my-cmd", server.Command)
	assert.Equal(t, []string{"my-arg"}, server.Args)
	assert.Equal(t, "my-env-val", server.Env["VAR"])
	assert.Equal(t, "http://example.com", server.URL)
	assert.Equal(t, "Bearer my-token", server.Headers["Authorization"])
}
