package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/menny/cassandra/llm"
)

const maxT3ToolOutputBytes = 40 * 1024

// IsT3Session returns true if Cassandra is currently running inside a T3 Code environment.
func IsT3Session() bool {
	return os.Getenv("T3_ACP_MCP_NODE") != ""
}

func callT3McpTool(ctx context.Context, tool string, args any) (string, error) {
	if !IsT3Session() {
		return "", fmt.Errorf("tool %q is only available in a T3 Code session", tool)
	}

	node := os.Getenv("T3_ACP_MCP_NODE")
	if node == "" {
		return "", fmt.Errorf("T3 MCP node executable (T3_ACP_MCP_NODE) is not configured")
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("failed to serialize args for %q: %w", tool, err)
	}

	var cmdArgs []string
	if entrypoint := os.Getenv("T3_ACP_MCP_ENTRYPOINT"); entrypoint != "" {
		cmdArgs = append(cmdArgs, entrypoint)
	}
	cmdArgs = append(cmdArgs, "acp-mcp-call", tool, string(argsJSON))
	cmd := exec.CommandContext(ctx, node, cmdArgs...)
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("calling T3 MCP tool %q failed: %w (output: %s)", tool, err, string(out))
	}
	result, err := unpackMCPTextEnvelope(out, fmt.Sprintf("T3 MCP tool %q", tool))
	if err != nil {
		return "", err
	}

	if len(result) > maxT3ToolOutputBytes {
		result = result[:maxT3ToolOutputBytes] + "\n... (truncated)"
	}
	return result, nil
}

// ModelSelection specifies provider instance and model parameters for thread launch.
type ModelSelection struct {
	InstanceID string `json:"instanceId,omitempty"`
	Model      string `json:"model"`
	Options    any    `json:"options,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

// WorkspaceStrategy specifies workspace configuration for a launched thread.
type WorkspaceStrategy struct {
	Type            string `json:"type"`
	BaseRef         string `json:"baseRef,omitempty"`
	Branch          string `json:"branch,omitempty"`
	StartFromOrigin *bool  `json:"startFromOrigin,omitempty"`
	WorktreePath    string `json:"worktreePath,omitempty"`
}

// ThreadLaunchArgs represents arguments passed to t3_thread_launch.
type ThreadLaunchArgs struct {
	Title             string             `json:"title"`
	Message           string             `json:"message,omitempty"`
	ModelSelection    *ModelSelection    `json:"modelSelection,omitempty"`
	WorkspaceStrategy *WorkspaceStrategy `json:"workspaceStrategy,omitempty"`
	RuntimeMode       string             `json:"runtimeMode,omitempty"`
	InteractionMode   string             `json:"interactionMode,omitempty"`
	ProjectID         string             `json:"projectId,omitempty"`
	Scratch           *bool              `json:"scratch,omitempty"`
}

// T3LaunchConfig specifies launch configuration for T3 Code sessions.
type T3LaunchConfig struct {
	PreferredHarnesses []string
	PreferredModel     string
	PromptForHarness   bool
}

// DefaultPreferredHarnesses defines standard priority of coding harnesses.
var DefaultPreferredHarnesses = []string{"antigravity", "opencode", "claudeAgent", "codex"}

// OrchestratorCapabilities mirrors T3's orchestrator_capabilities payload.
type OrchestratorCapabilities struct {
	ParentThreadID              string               `json:"parentThreadId"`
	InheritedProviderInstanceID string               `json:"inheritedProviderInstanceId"`
	InheritedModel              string               `json:"inheritedModel"`
	InheritedOptions            any                  `json:"inheritedOptions"`
	RuntimeMode                 string               `json:"runtimeMode"`
	InteractionMode             string               `json:"interactionMode"`
	Providers                   []ProviderCapability `json:"providers"`
}

// ProviderCapability describes a provider instance in orchestrator_capabilities.
type ProviderCapability struct {
	ProviderInstanceID           string            `json:"providerInstanceId"`
	DriverKind                   string            `json:"driverKind"`
	DisplayName                  string            `json:"displayName"`
	Models                       []CapabilityModel `json:"models"`
	CanRunChildTask              bool              `json:"canRunChildTask"`
	CanRunCrossProviderChildTask bool              `json:"canRunCrossProviderChildTask"`
	Constraints                  []string          `json:"constraints"`
}

// CapabilityModel describes a model offered by a provider instance.
type CapabilityModel struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// CodingHarnessChoice describes a candidate coding harness option for the developer.
type CodingHarnessChoice struct {
	InstanceID  string `json:"instanceId"`
	DisplayName string `json:"displayName"`
	ModelID     string `json:"model"`
	ModelLabel  string `json:"modelLabel"`
	IsPreferred bool   `json:"isPreferred"`
}

// MaskCassandraInCapabilities marks Cassandra as non-child-task and read-only in orchestrator_capabilities output.
func MaskCassandraInCapabilities(raw string) string {
	var caps OrchestratorCapabilities
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return raw
	}
	modified := false
	for i := range caps.Providers {
		p := &caps.Providers[i]
		if strings.EqualFold(p.ProviderInstanceID, "cassandra") || strings.EqualFold(p.DisplayName, "cassandra") {
			p.CanRunChildTask = false
			p.CanRunCrossProviderChildTask = false
			p.Constraints = append(p.Constraints, "Cassandra is a read-only code review agent and cannot execute code fixes.")
			modified = true
		}
	}
	if !modified {
		return raw
	}
	out, err := json.Marshal(caps)
	if err != nil {
		return raw
	}
	return string(out)
}

// FilterEnabledCodingProviders filters out Cassandra, read-only providers, and disabled providers.
func FilterEnabledCodingProviders(providers []ProviderCapability) []ProviderCapability {
	var enabled []ProviderCapability
	for _, p := range providers {
		if strings.EqualFold(p.ProviderInstanceID, "cassandra") || strings.EqualFold(p.DisplayName, "cassandra") {
			continue
		}
		if !p.CanRunChildTask {
			continue
		}
		isDisabled := false
		for _, c := range p.Constraints {
			lower := strings.ToLower(c)
			if strings.Contains(lower, "disabled") || strings.Contains(lower, "read-only") {
				isDisabled = true
				break
			}
		}
		if isDisabled {
			continue
		}
		enabled = append(enabled, p)
	}
	return enabled
}

func pickModelForProvider(p *ProviderCapability, requestedModel, preferredModel, inheritedModel string, options any) *ModelSelection {
	sel := &ModelSelection{
		InstanceID: p.ProviderInstanceID,
		Options:    options,
	}

	// 1. Requested model exists in provider models
	if requestedModel != "" {
		for _, m := range p.Models {
			if m.ID == requestedModel {
				sel.Model = requestedModel
				return sel
			}
		}
	}

	// 2. Preferred model from TOML exists in provider models
	if preferredModel != "" {
		for _, m := range p.Models {
			if m.ID == preferredModel {
				sel.Model = preferredModel
				return sel
			}
		}
	}

	// 3. Inherited model exists in provider models
	if inheritedModel != "" {
		for _, m := range p.Models {
			if m.ID == inheritedModel {
				sel.Model = inheritedModel
				return sel
			}
		}
	}

	// 4. Default to first available model for provider
	if len(p.Models) > 0 {
		sel.Model = p.Models[0].ID
	} else if requestedModel != "" {
		sel.Model = requestedModel
	} else if preferredModel != "" {
		sel.Model = preferredModel
	} else {
		sel.Model = "default"
	}

	return sel
}

// ResolveLaunchHarness deterministically picks an enabled coding harness and model.
func ResolveLaunchHarness(capabilitiesRaw string, cfg T3LaunchConfig, requested *ModelSelection) (*ModelSelection, error) {
	var caps OrchestratorCapabilities
	if err := json.Unmarshal([]byte(capabilitiesRaw), &caps); err != nil {
		if requested != nil && requested.InstanceID != "" && !strings.EqualFold(requested.InstanceID, "cassandra") {
			return requested, nil
		}
		return nil, fmt.Errorf("failed to parse orchestrator capabilities: %w", err)
	}

	enabled := FilterEnabledCodingProviders(caps.Providers)
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no enabled coding agent harnesses available in T3 Code")
	}

	// 1. If caller requested a specific valid coding harness (not cassandra), honor it if enabled.
	if requested != nil && requested.InstanceID != "" && !strings.EqualFold(requested.InstanceID, "cassandra") {
		for _, p := range enabled {
			if strings.EqualFold(p.ProviderInstanceID, requested.InstanceID) {
				return pickModelForProvider(&p, requested.Model, cfg.PreferredModel, caps.InheritedModel, requested.Options), nil
			}
		}
	}

	// 2. Priority match against preferred harnesses.
	preferred := cfg.PreferredHarnesses
	if len(preferred) == 0 {
		preferred = DefaultPreferredHarnesses
	}

	for _, pref := range preferred {
		for _, p := range enabled {
			if strings.EqualFold(p.ProviderInstanceID, pref) {
				var reqModel string
				var reqOptions any
				if requested != nil {
					reqModel = requested.Model
					reqOptions = requested.Options
				}
				return pickModelForProvider(&p, reqModel, cfg.PreferredModel, caps.InheritedModel, reqOptions), nil
			}
		}
	}

	// 3. Fallback: match inheritedProviderInstanceId if enabled and not cassandra.
	if caps.InheritedProviderInstanceID != "" && !strings.EqualFold(caps.InheritedProviderInstanceID, "cassandra") {
		for _, p := range enabled {
			if strings.EqualFold(p.ProviderInstanceID, caps.InheritedProviderInstanceID) {
				var reqModel string
				var reqOptions any
				if requested != nil {
					reqModel = requested.Model
					reqOptions = requested.Options
				}
				return pickModelForProvider(&p, reqModel, cfg.PreferredModel, caps.InheritedModel, reqOptions), nil
			}
		}
	}

	// 4. Ultimate fallback: pick first enabled coding provider.
	p := &enabled[0]
	var reqModel string
	var reqOptions any
	if requested != nil {
		reqModel = requested.Model
		reqOptions = requested.Options
	}
	return pickModelForProvider(p, reqModel, cfg.PreferredModel, caps.InheritedModel, reqOptions), nil
}

// GetAvailableCodingHarnessChoices inspects capabilities and returns candidate coding choices.
func GetAvailableCodingHarnessChoices(capabilitiesRaw string, cfg T3LaunchConfig) []CodingHarnessChoice {
	var caps OrchestratorCapabilities
	if err := json.Unmarshal([]byte(capabilitiesRaw), &caps); err != nil {
		return nil
	}
	enabled := FilterEnabledCodingProviders(caps.Providers)
	if len(enabled) == 0 {
		return nil
	}

	bestMatch, _ := ResolveLaunchHarness(capabilitiesRaw, cfg, nil)
	var bestInstanceID string
	if bestMatch != nil {
		bestInstanceID = bestMatch.InstanceID
	}

	var choices []CodingHarnessChoice
	for _, p := range enabled {
		isPref := false
		if bestInstanceID != "" && strings.EqualFold(p.ProviderInstanceID, bestInstanceID) {
			isPref = true
		}
		sel := pickModelForProvider(&p, "", cfg.PreferredModel, caps.InheritedModel, nil)
		label := sel.Model
		for _, m := range p.Models {
			if m.ID == sel.Model && m.Label != "" {
				label = m.Label
				break
			}
		}
		choices = append(choices, CodingHarnessChoice{
			InstanceID:  p.ProviderInstanceID,
			DisplayName: p.DisplayName,
			ModelID:     sel.Model,
			ModelLabel:  label,
			IsPreferred: isPref,
		})
	}
	return choices
}

// FormatHarnessOptionsForDeveloper converts candidate choices to string options for ask_developer.
func FormatHarnessOptionsForDeveloper(choices []CodingHarnessChoice) []string {
	var result []string
	for _, c := range choices {
		name := c.DisplayName
		if name == "" {
			name = c.InstanceID
		}
		prefix := ""
		if c.IsPreferred {
			prefix = "(Recommended) "
		}
		result = append(result, fmt.Sprintf("%s%s — %s", prefix, name, c.ModelLabel))
	}
	return result
}

func registerT3Tools(r *Registry) {
	// Only expose T3-specific tools when running inside a T3 Code environment.
	if !IsT3Session() {
		return
	}

	linkDef := llm.ToolDef{
		Name:        "link_pull_request",
		Description: "Register every pull request you open for this thread, including each layer of a stack, right after creating it. Links a pull request to this thread so T3 Code tracks it, shows its status beside the thread, and settles the thread when it merges. Pass the URL, or repository plus number. Linking an already-linked pull request succeeds with alreadyLinked=true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Full pull request URL.",
				},
				"repository": map[string]any{
					"type":        "string",
					"description": "Owner and repository name.",
				},
				"number": map[string]any{
					"type":        "integer",
					"description": "Pull request number.",
				},
			},
		},
	}

	type linkArgs struct {
		URL        string `json:"url"`
		Repository string `json:"repository"`
		Number     int    `json:"number"`
	}

	RegisterToolWithArgs(r, linkDef, func(ctx context.Context, args linkArgs) (string, error) {
		return callT3McpTool(ctx, "link_pull_request", args)
	})

	listDef := llm.ToolDef{
		Name:        "list_thread_pull_requests",
		Description: "List the pull requests linked to a thread with their last known host state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"threadId": map[string]any{
					"type":        "string",
					"description": "Thread ID. Omit for the current thread.",
				},
			},
		},
	}

	type listArgs struct {
		ThreadID string `json:"threadId"`
	}

	RegisterToolWithArgs(r, listDef, func(ctx context.Context, args listArgs) (string, error) {
		return callT3McpTool(ctx, "list_thread_pull_requests", args)
	})

	capDef := llm.ToolDef{
		Name:        "orchestrator_capabilities",
		Description: "List the V2 provider instances and their current models from the same live catalog as the composer, including configured custom models, inherited runtime settings, and app-owned orchestration features available to this caller.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}

	type capArgs struct{}

	RegisterToolWithArgs(r, capDef, func(ctx context.Context, args capArgs) (string, error) {
		raw, err := callT3McpTool(ctx, "orchestrator_capabilities", args)
		if err != nil {
			return "", err
		}
		return MaskCassandraInCapabilities(raw), nil
	})

	launchDef := llm.ToolDef{
		Name:        "t3_thread_launch",
		Description: "Create a new top-level thread in T3 Code to carry out tasks, such as addressing code review findings. Use this tool when explicitly asked by the user, or after the user agrees to spawn a fix session.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Title of the thread, formatted as 'Review Fix [short description]'.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Initial task instructions for the spawned thread, including context, user directions, and technical fix guidelines.",
				},
				"modelSelection": map[string]any{
					"type":        "object",
					"description": "Model and provider selection. Use orchestrator_capabilities to pick instanceId and model.",
					"properties": map[string]any{
						"instanceId": map[string]any{
							"type":        "string",
							"description": "The provider instance ID (e.g., from orchestrator_capabilities). Must be an active coding agent harness (e.g. 'antigravity', 'opencode'), never 'cassandra'.",
						},
						"model": map[string]any{
							"type":        "string",
							"description": "The model ID selected from that provider instance's available models.",
						},
					},
					"required": []string{"model"},
				},
				"workspaceStrategy": map[string]any{
					"type":        "object",
					"description": "Optional workspace strategy (e.g. worktree, existing_worktree, root). Defaults to project root.",
				},
			},
			"required": []string{"title", "message"},
		},
	}

	RegisterToolWithArgs(r, launchDef, func(ctx context.Context, args ThreadLaunchArgs) (string, error) {
		args.Title = normalizeReviewFixTitle(args.Title)

		capOut, err := callT3McpTool(ctx, "orchestrator_capabilities", capArgs{})
		if err != nil {
			return "", fmt.Errorf("failed to query orchestrator capabilities: %w", err)
		}

		// Prompt developer if harness selection is missing, prompt-for-harness is true, and a prompter is available
		needsPrompt := args.ModelSelection == nil ||
			args.ModelSelection.InstanceID == "" ||
			strings.EqualFold(args.ModelSelection.InstanceID, "cassandra")

		if needsPrompt && r.t3Config.PromptForHarness && r.prompter != nil {
			choices := GetAvailableCodingHarnessChoices(capOut, r.t3Config)
			if len(choices) > 1 {
				formatted := FormatHarnessOptionsForDeveloper(choices)
				resp, pErr := r.prompter.AskDeveloper(ctx, AskDeveloperRequest{
					Question:      "Which coding agent harness would you like to use for this fix session?",
					Reasoning:     "Cassandra is a read-only review agent. A coding agent harness must be selected to modify files and run tests.",
					Options:       formatted,
					IsMultiSelect: false,
				})
				if pErr == nil && resp.Status == "answered" && len(resp.Selected) > 0 {
					selectedText := resp.Selected[0]
					cleanFormatted := strings.TrimSpace(strings.TrimPrefix(selectedText, "(Recommended) "))
					for _, c := range choices {
						name := c.DisplayName
						if name == "" {
							name = c.InstanceID
						}
						expectedFormatted := fmt.Sprintf("%s — %s", name, c.ModelLabel)
						if cleanFormatted == expectedFormatted || cleanFormatted == name {
							args.ModelSelection = &ModelSelection{
								InstanceID: c.InstanceID,
								Model:      c.ModelID,
							}
							break
						}
					}
				}
			}
		}

		// Deterministic resolution & validation for all launch calls
		resolved, rErr := ResolveLaunchHarness(capOut, r.t3Config, args.ModelSelection)
		if rErr != nil {
			return "", fmt.Errorf("failed to resolve coding agent harness: %w", rErr)
		}
		if resolved == nil || resolved.InstanceID == "" || strings.EqualFold(resolved.InstanceID, "cassandra") {
			return "", fmt.Errorf("failed to resolve a valid coding agent harness: Cassandra cannot be used to execute fixes")
		}
		args.ModelSelection = resolved

		return callT3McpTool(ctx, "t3_thread_launch", args)
	})
}

const (
	// ReviewStatePending indicates that the review or pre-review is in progress.
	ReviewStatePending = "⏳"
	// ReviewStateApproved indicates that the review concluded with an approval.
	ReviewStateApproved = "✅"
	// ReviewStateRejected indicates that the review concluded with a rejection.
	ReviewStateRejected = "❎"
	// ReviewStateElse indicates non-blocking feedback, uncertain outcome, or general completion.
	ReviewStateElse = "✳️"
)

var reviewStatusPrefixRegex = regexp.MustCompile(`^(?:(?:⏳|✅|❎|❌|💬|✳️|✳)\s*)+`)

// CleanThreadTitle strips existing review status emoji prefixes (and following spaces)
// from a thread title.
func CleanThreadTitle(title string) string {
	title = strings.TrimSpace(title)
	return strings.TrimSpace(reviewStatusPrefixRegex.ReplaceAllString(title, ""))
}

// FormatThreadTitle formats a thread title with the specified review status emoji prefix.
func FormatThreadTitle(emoji, baseTitle string) string {
	clean := CleanThreadTitle(baseTitle)
	if clean == "" {
		clean = "Review"
	}
	return fmt.Sprintf("%s %s", emoji, clean)
}

// GetCurrentThreadTitle returns the title of the current T3 thread if running in a T3 session.
func GetCurrentThreadTitle(ctx context.Context) (string, error) {
	if !IsT3Session() {
		return "", fmt.Errorf("not in a T3 Code session")
	}

	out, err := callT3McpTool(ctx, "t3_thread_list", map[string]any{"limit": 50})
	if err != nil {
		return "", err
	}

	type threadItem struct {
		ThreadID string `json:"threadId"`
		ID       string `json:"id"`
		Title    string `json:"title"`
	}

	var listResp struct {
		CurrentThreadID   string       `json:"currentThreadId"`
		Threads           []threadItem `json:"threads"`
		StructuredContent *struct {
			CurrentThreadID string       `json:"currentThreadId"`
			Threads         []threadItem `json:"threads"`
		} `json:"structuredContent,omitempty"`
	}
	if err := json.Unmarshal([]byte(out), &listResp); err != nil {
		return "", fmt.Errorf("failed to parse t3_thread_list response: %w", err)
	}

	if listResp.StructuredContent != nil {
		if listResp.CurrentThreadID == "" {
			listResp.CurrentThreadID = listResp.StructuredContent.CurrentThreadID
		}
		if len(listResp.Threads) == 0 {
			listResp.Threads = listResp.StructuredContent.Threads
		}
	}

	for _, th := range listResp.Threads {
		if (th.ThreadID != "" && th.ThreadID == listResp.CurrentThreadID) || (th.ID != "" && th.ID == listResp.CurrentThreadID) {
			return th.Title, nil
		}
	}

	if listResp.CurrentThreadID != "" {
		readOut, err := callT3McpTool(ctx, "t3_thread_read", map[string]any{
			"threadId": listResp.CurrentThreadID,
			"limit":    1,
		})
		if err == nil {
			var readResp struct {
				Thread struct {
					Title string `json:"title"`
				} `json:"thread"`
				StructuredContent *struct {
					Thread struct {
						Title string `json:"title"`
					} `json:"thread"`
				} `json:"structuredContent,omitempty"`
			}
			if json.Unmarshal([]byte(readOut), &readResp) == nil {
				if readResp.Thread.Title != "" {
					return readResp.Thread.Title, nil
				}
				if readResp.StructuredContent != nil && readResp.StructuredContent.Thread.Title != "" {
					return readResp.StructuredContent.Thread.Title, nil
				}
			}
		}
	}

	if len(listResp.Threads) == 1 {
		return listResp.Threads[0].Title, nil
	}

	return "", fmt.Errorf("current thread %q not found in thread list", listResp.CurrentThreadID)
}

// UpdateThreadTitlePrefix updates the current T3 thread title with the specified review status emoji prefix.
// If not in a T3 session, it is a no-op and returns nil.
func UpdateThreadTitlePrefix(ctx context.Context, emoji string) error {
	if !IsT3Session() {
		return nil
	}

	currentTitle, err := GetCurrentThreadTitle(ctx)
	if err != nil {
		// If getting the current title fails, fall back to "Review" as the base title
		// so that the title update still happens instead of failing silently.
		currentTitle = "Review"
	}

	newTitle := FormatThreadTitle(emoji, currentTitle)
	if newTitle == currentTitle {
		return nil
	}

	_, err = callT3McpTool(ctx, "t3_thread_update", map[string]any{
		"action": "rename",
		"title":  newTitle,
	})
	return err
}
