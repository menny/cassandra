package mcp

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpandEnv(t *testing.T) {
	os.Setenv("TEST_CMD", "my-cmd")
	os.Setenv("TEST_ARG", "my-arg")
	os.Setenv("TEST_ENV_VAL", "my-env-val")
	os.Setenv("TEST_URL", "http://example.com")
	os.Setenv("TEST_HEADER", "Bearer my-token")
	defer func() {
		os.Unsetenv("TEST_CMD")
		os.Unsetenv("TEST_ARG")
		os.Unsetenv("TEST_ENV_VAL")
		os.Unsetenv("TEST_URL")
		os.Unsetenv("TEST_HEADER")
	}()

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
