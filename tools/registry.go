package tools

import (
	"fmt"

	"github.com/menny/cassandra/llm"
)

type ToolHandler func(args map[string]any) (string, error)

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

func (r *Registry) HandleCall(name string, args map[string]any) (string, error) {
	handler, ok := r.handlers[name]
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	return handler(args)
}

func RegisterLocalTools(r *Registry) {
	registerLocalReadFile(r)
	registerLocalGlobFiles(r)
}
