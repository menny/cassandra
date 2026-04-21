package mcp

import (
	"os"
)

type ServerConfig struct {
	// For Stdio servers
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// For HTTP/SSE network servers
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ExpandEnv recursively applies os.ExpandEnv to all string values within the Config.
func (c *Config) ExpandEnv() {
	for name, server := range c.MCPServers {
		server.Command = os.ExpandEnv(server.Command)
		for i, arg := range server.Args {
			server.Args[i] = os.ExpandEnv(arg)
		}
		if server.Env != nil {
			for k, v := range server.Env {
				server.Env[k] = os.ExpandEnv(v)
			}
		}
		server.URL = os.ExpandEnv(server.URL)
		if server.Headers != nil {
			for k, v := range server.Headers {
				server.Headers[k] = os.ExpandEnv(v)
			}
		}
		c.MCPServers[name] = server
	}
}
