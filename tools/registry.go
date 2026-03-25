package tools

import (
	"fmt"
	"github.com/tmc/langchaingo/llms"
)

type ToolHandler func(args map[string]any) (string, error)

type Registry struct {
	tools    []llms.Tool
	handlers map[string]ToolHandler
}

func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]ToolHandler),
	}
}

func (r *Registry) ToLangChainTools() []llms.Tool {
	return r.tools
}

func (r *Registry) RegisterTool(def llms.Tool, handler ToolHandler) {
	r.tools = append(r.tools, def)
	if def.Function != nil {
		r.handlers[def.Function.Name] = handler
	}
}

func (r *Registry) HandleCall(name string, args map[string]any) (string, error) {
	handler, ok := r.handlers[name]
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	return handler(args)
}

func RegisterLocalTools(r *Registry, diffBranch string) {
	registerLocalReadFile(r)
	registerLocalGlobFiles(r)
}

func RegisterPRTools(r *Registry, prNumber int) {
	// TODO: Register tools for remote PRs (e.g. fetching directly from GH API)
}
