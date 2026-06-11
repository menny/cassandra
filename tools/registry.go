package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/menny/cassandra/llm"
)

type ToolHandler func(ctx context.Context, tc llm.ToolCall) (string, error)

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

func (r *Registry) HandleCall(ctx context.Context, tc llm.ToolCall) (string, error) {
	handler, ok := r.handlers[tc.Name]
	if !ok {
		return "", fmt.Errorf("tool not found: %s", tc.Name)
	}
	return handler(ctx, tc)
}

// RegisterToolWithArgs registers a tool that uses a specific struct for its
// arguments. It handles unmarshaling the arguments and returns an error if
// they are malformed.
func RegisterToolWithArgs[T any](r *Registry, def llm.ToolDef, handler func(context.Context, T) (string, error)) {
	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args T
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", err
		}
		return handler(ctx, args)
	})
}

func RegisterLocalTools(r *Registry, root string, ignoredLockFiles []string, wishlistDir string, allowAskDeveloper bool) {
	if root != "" {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	}
	registerLocalReadFile(r, root)
	registerLocalGlobFiles(r, root)
	registerLocalGrepFiles(r, root, ignoredLockFiles)
	registerWishlistTool(r, wishlistDir)
	registerEmitReviewerState(r)
	if allowAskDeveloper {
		registerAskDeveloper(r)
	}
}
