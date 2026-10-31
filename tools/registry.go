package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/menny/cassandra/llm"
)

type ToolHandler func(ctx context.Context, tc llm.ToolCall) (string, error)

// VerdictChangeListener is invoked whenever the review verdict is updated via update_review_verdict.
type VerdictChangeListener func(ctx context.Context, verdict string, rationale string) error

type Registry struct {
	tools           []llm.ToolDef
	handlers        map[string]ToolHandler
	verdictListener VerdictChangeListener
}

func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]ToolHandler),
	}
}

func (r *Registry) ToTools() []llm.ToolDef {
	return r.tools
}

func (r *Registry) SetVerdictChangeListener(l VerdictChangeListener) {
	r.verdictListener = l
}

func (r *Registry) RegisterTool(def llm.ToolDef, handler ToolHandler) {
	if _, exists := r.handlers[def.Name]; exists {
		for i, existing := range r.tools {
			if existing.Name == def.Name {
				r.tools[i] = def
				break
			}
		}
	} else {
		r.tools = append(r.tools, def)
	}
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

func RegisterLocalTools(r *Registry, root, base, head string, ignoredLockFiles []string, wishlistDir string, allowAskDeveloper bool, allowPRComment bool, notifier UserNotifier, diffMapProvider func() map[string]string, metadataJSONFile ...string) {
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
	registerLocalGetFileDiff(r, root, base, head, ignoredLockFiles, diffMapProvider)
	registerWishlistTool(r, wishlistDir)
	registerEmitReviewerState(r)
	registerUpdateReviewVerdict(r)
	registerT3Tools(r)
	registerGetPRMetadataTool(r, root, metaFile, ignoredLockFiles)
	registerGetPRDiffTool(r, root, metaFile, ignoredLockFiles)
	if allowPRComment {
		registerPRCommentTool(r, root, metaFile)
	}
	if allowAskDeveloper {
		registerAskDeveloper(r, notifier)
	}
}
