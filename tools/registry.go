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
		return "", fmt.Errorf("tool %q not found", tc.Name)
	}
	return handler(ctx, tc)
}

func RegisterToolWithArgs[T any](r *Registry, def llm.ToolDef, handler func(ctx context.Context, args T) (string, error)) {
	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args T
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", fmt.Errorf("unmarshaling %s arguments: %w", def.Name, err)
		}
		return handler(ctx, args)
	})
}

type UserNotifier interface {
	NotifyUser()
}

type NoOpUserNotifier struct{}

func (NoOpUserNotifier) NotifyUser() {}

func RegisterLocalTools(r *Registry, root string, ignoredLockFiles []string, wishlistDir string, allowAskDeveloper bool, notifier UserNotifier, metadataJSONFile ...string) {
	if notifier == nil {
		notifier = NoOpUserNotifier{}
	}
	if root != "" {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	}
	var metaFile string
	if len(metadataJSONFile) > 0 {
		metaFile = metadataJSONFile[0]
	}
	registerLocalReadFile(r, root)
	registerLocalGlobFiles(r, root)
	registerLocalGrepFiles(r, root, ignoredLockFiles)
	registerWishlistTool(r, wishlistDir)
	registerEmitReviewerState(r)
	registerT3Tools(r)
	registerPRCommentTool(r, root, metaFile)
	if allowAskDeveloper {
		registerAskDeveloper(r, notifier)
	}
}
