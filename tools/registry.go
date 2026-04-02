package tools

import (
	"fmt"

	"github.com/menny/cassandra/llm"
)

type ToolHandler func(tc llm.ToolCall) (string, error)

type Registry struct {
	tools    []llm.ToolDef
	handlers map[string]ToolHandler
}

func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]ToolHandler),
	}
}

func (r *Registry) ToTools() []llm.ToolDef {
	return r.tools
}

func (r *Registry) RegisterTool(def llm.ToolDef, handler ToolHandler) {
	r.tools = append(r.tools, def)
	r.handlers[def.Name] = handler
}

func (r *Registry) HandleCall(tc llm.ToolCall) (string, error) {
	handler, ok := r.handlers[tc.Name]
	if !ok {
		return "", fmt.Errorf("tool not found: %s", tc.Name)
	}
	return handler(tc)
}

func RegisterLocalTools(r *Registry) {
	registerLocalReadFile(r)
	registerLocalGlobFiles(r)
	registerLocalGrepFiles(r)
}
