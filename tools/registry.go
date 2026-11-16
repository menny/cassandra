package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/menny/cassandra/llm"
)

type ToolHandler func(ctx context.Context, tc llm.ToolCall) (string, error)

// Dispatcher enumerates tools and dispatches calls within an execution scope.
type Dispatcher interface {
	ToTools() []llm.ToolDef
	HandleCall(context.Context, llm.ToolCall) (string, error)
}

// ToolPolicy controls dispatch permissions without exposing policy to the model.
// An omitted policy preserves legacy built-in access (AllowSubagents: true).
// An explicit policy is used as-is, so its zero value denies subagent access.
// Register MCP tools with ToolPolicy{AllowSubagents: true, IsMCP: true} to
// permit them only when the subagent also opts into MCP access.
type ToolPolicy struct {
	AllowSubagents bool
	IsMCP          bool
}

// VerdictChangeListener is invoked whenever the review verdict is updated via update_review_verdict.
type VerdictChangeListener func(ctx context.Context, verdict string, rationale string) error

// Registry must be fully configured before dispatching concurrently. Registration
// and configuration are not synchronized; subagent views only read this state.
type Registry struct {
	tools           []llm.ToolDef
	handlers        map[string]ToolHandler
	policies        map[string]ToolPolicy
	verdictListener VerdictChangeListener
	prompter        DeveloperPrompter
	t3Config        T3LaunchConfig
	omnigentConfig  OmnigentLaunchConfig
}

func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]ToolHandler),
		policies: make(map[string]ToolPolicy),
	}
}

func (r *Registry) ToTools() []llm.ToolDef {
	return r.tools
}

func (r *Registry) SetVerdictChangeListener(l VerdictChangeListener) {
	r.verdictListener = l
}

func (r *Registry) SetDeveloperPrompter(p DeveloperPrompter) {
	r.prompter = p
}

func (r *Registry) SetT3Config(cfg T3LaunchConfig) {
	r.t3Config = cfg
}

func (r *Registry) SetOmnigentConfig(cfg OmnigentLaunchConfig) {
	r.omnigentConfig = cfg
}

// RegisterTool registers or replaces a tool and its handler. Supply at most one
// policy; omitting it preserves an existing policy or allows a new built-in tool.
// MCP registrations cannot reopen a previously denied tool name.
func (r *Registry) RegisterTool(def llm.ToolDef, handler ToolHandler, policy ...ToolPolicy) {
	p, hasPolicy := r.policies[def.Name]
	if !hasPolicy {
		p = ToolPolicy{AllowSubagents: true}
	}
	if len(policy) > 0 {
		explicit := policy[0]
		if explicit.IsMCP && hasPolicy && !p.AllowSubagents {
			explicit.AllowSubagents = false
		}
		p = explicit
	}
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
	r.policies[def.Name] = p
}

func (r *Registry) HandleCall(ctx context.Context, tc llm.ToolCall) (string, error) {
	handler, ok := r.handlers[tc.Name]
	if !ok {
		return "", fmt.Errorf("tool %q not found", tc.Name)
	}
	return handler(ctx, tc)
}

// RegisterToolWithArgs decodes arguments before dispatching and accepts the same
// optional registration policy as RegisterTool.
func RegisterToolWithArgs[T any](r *Registry, def llm.ToolDef, handler func(ctx context.Context, args T) (string, error), policy ...ToolPolicy) {
	r.RegisterTool(def, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		var args T
		if err := tc.UnmarshalArguments(&args); err != nil {
			return "", fmt.Errorf("unmarshaling %s arguments: %w", def.Name, err)
		}
		return handler(ctx, args)
	}, policy...)
}

// SubagentRegistry is a read-only view that filters discovery and enforces policy
// at dispatch, including calls to tool names omitted from discovery. It satisfies
// the same ToTools/HandleCall dispatcher interface as Registry without importing core.
type SubagentRegistry struct {
	registry  *Registry
	allowMCPs bool
}

// ForSubagent creates an independent view after all tools have been registered.
// MCP opt-in never overrides a tool's AllowSubagents: false policy. Views may be
// used concurrently provided the underlying registry is no longer modified.
func (r *Registry) ForSubagent(allowMCPs bool) Dispatcher {
	return &SubagentRegistry{registry: r, allowMCPs: allowMCPs}
}

func (r *SubagentRegistry) allowed(name string) bool {
	p, ok := r.registry.policies[name]
	return ok && p.AllowSubagents && (!p.IsMCP || r.allowMCPs)
}

// ToTools returns a fresh slice containing only tools available to this subagent.
// Tool definitions and their parameter schemas must be treated as immutable.
func (r *SubagentRegistry) ToTools() []llm.ToolDef {
	var defs []llm.ToolDef
	for _, def := range r.registry.tools {
		if r.allowed(def.Name) {
			defs = append(defs, def)
		}
	}
	return defs
}

// HandleCall rejects disallowed or unknown names before invoking any handler.
// Allowed calls retain the caller's context, arguments, and handler errors.
func (r *SubagentRegistry) HandleCall(ctx context.Context, tc llm.ToolCall) (string, error) {
	if _, ok := r.registry.handlers[tc.Name]; !ok {
		return "", fmt.Errorf("tool %q not found", tc.Name)
	}
	if !r.allowed(tc.Name) {
		return "", fmt.Errorf("tool %q is not allowed for subagents", tc.Name)
	}
	return r.registry.HandleCall(ctx, tc)
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
	registerGitLog(r, root)
	registerGitBlame(r, root)
	registerGitShow(r, root, ignoredLockFiles)
	registerGitDiffStat(r, root, ignoredLockFiles)
	registerWishlistTool(r, wishlistDir)
	registerEmitReviewerState(r)
	registerUpdateReviewVerdict(r)
	registerT3Tools(r)
	registerOmnigentTools(r)
	registerGetPRMetadataTool(r, root, metaFile, ignoredLockFiles)
	registerGetPRDiffTool(r, root, metaFile, ignoredLockFiles)
	registerGetPRCommentTool(r, root, metaFile)
	registerGetIssueTool(r, root, metaFile)
	registerListPRCommitsTool(r, root, metaFile)
	registerGetCheckRunsTool(r, root, metaFile)
	registerGetWorkflowRunLogsTool(r, root, metaFile)
	registerGetFileAtRefTool(r, root, metaFile)
	registerGetPRStackTool(r, root, metaFile)
	if allowPRComment {
		registerPRCommentTool(r, root, metaFile)
	}
	if allowAskDeveloper {
		prompter := r.prompter
		if prompter == nil {
			if p, ok := notifier.(DeveloperPrompter); ok {
				prompter = p
			}
		}
		registerAskDeveloper(r, prompter, notifier)
	}
}
