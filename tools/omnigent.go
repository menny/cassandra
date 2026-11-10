package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/menny/cassandra/llm"
)

const maxOmnigentToolOutputBytes = 40 * 1024

// IsOmnigentSession returns true if Cassandra is currently running inside an Omnigent environment.
func IsOmnigentSession() bool {
	if os.Getenv("OMNIGENT") != "" || os.Getenv("OMNIGENT_RUNNER_WORKSPACE") != "" || os.Getenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID") != "" {
		return true
	}
	if os.Getenv("OMNIGENT_RELAY_URL") != "" {
		return true
	}
	if os.Getenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR") != "" || os.Getenv("OMNIGENT_BRIDGE_DIR") != "" {
		return true
	}
	return false
}

type omnigentRelayConfig struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	SessionID string `json:"session_id,omitempty"`
}

func readRelayConfigFile(path string) (*omnigentRelayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg omnigentRelayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" || cfg.Token == "" {
		return nil, errors.New("relay config missing url or token")
	}
	return &cfg, nil
}

func readRelayEnvFile(path string) (*omnigentRelayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var url, token string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "OMNIGENT_RELAY_URL=") {
			val := strings.TrimPrefix(line, "OMNIGENT_RELAY_URL=")
			url = strings.Trim(val, `"' `)
		} else if strings.HasPrefix(line, "OMNIGENT_RELAY_TOKEN=") {
			val := strings.TrimPrefix(line, "OMNIGENT_RELAY_TOKEN=")
			token = strings.Trim(val, `"' `)
		}
	}
	if url == "" || token == "" {
		return nil, errors.New("relay env missing url or token")
	}
	return &omnigentRelayConfig{URL: url, Token: token}, nil
}

func findActiveToolRelay(omnigentRoot string, targetSessionID string) *omnigentRelayConfig {
	var candidates []string
	matches, err := filepath.Glob(filepath.Join(omnigentRoot, "*", "*", "tool_relay.json"))
	if err == nil {
		candidates = append(candidates, matches...)
	}

	// Sort candidates by modification time (newest first)
	type fileCandidate struct {
		path    string
		modTime time.Time
	}
	var fileList []fileCandidate
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err == nil {
			fileList = append(fileList, fileCandidate{path: c, modTime: info.ModTime()})
		}
	}
	sort.Slice(fileList, func(i, j int) bool {
		return fileList[i].modTime.After(fileList[j].modTime)
	})

	for _, fc := range fileList {
		cfg, err := readRelayConfigFile(fc.path)
		if err != nil {
			continue
		}
		if targetSessionID != "" && cfg.SessionID != "" {
			if cfg.SessionID == targetSessionID {
				return cfg
			}
		} else {
			return cfg
		}
	}

	// Fallback to checking tool_relay.env
	envMatches, _ := filepath.Glob(filepath.Join(omnigentRoot, "*", "*", "tool_relay.env"))
	for _, em := range envMatches {
		cfg, err := readRelayEnvFile(em)
		if err == nil {
			return cfg
		}
	}

	return nil
}

func resolveOmnigentRelay() (*omnigentRelayConfig, error) {
	url := os.Getenv("OMNIGENT_RELAY_URL")
	token := os.Getenv("OMNIGENT_RELAY_TOKEN")
	sid := os.Getenv("OMNIGENT_RUNNER_PRIMARY_SESSION_ID")
	if url != "" && token != "" {
		return &omnigentRelayConfig{URL: url, Token: token, SessionID: sid}, nil
	}

	bridgeDir := os.Getenv("OMNIGENT_BRIDGE_DIR")
	if bridgeDir == "" {
		bridgeDir = os.Getenv("HARNESS_CLAUDE_NATIVE_BRIDGE_DIR")
	}
	if bridgeDir != "" {
		if cfg, err := readRelayConfigFile(filepath.Join(bridgeDir, "tool_relay.json")); err == nil {
			return cfg, nil
		}
		if cfg, err := readRelayEnvFile(filepath.Join(bridgeDir, "tool_relay.env")); err == nil {
			return cfg, nil
		}
	}

	home, err := os.UserHomeDir()
	if err == nil {
		omnigentRoot := filepath.Join(home, ".omnigent")
		if cfg := findActiveToolRelay(omnigentRoot, sid); cfg != nil {
			return cfg, nil
		}
	}

	return nil, errors.New("omnigent tool relay is not configured or reachable")
}

func callOmnigentTool(ctx context.Context, tool string, args any) (string, error) {
	if !IsOmnigentSession() {
		return "", fmt.Errorf("tool %q is only available in an Omnigent session", tool)
	}

	relay, err := resolveOmnigentRelay()
	if err != nil {
		return "", fmt.Errorf("calling Omnigent tool %q failed: %w", tool, err)
	}

	reqPayload := map[string]any{
		"name":      tool,
		"arguments": args,
	}
	reqBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("failed to serialize args for %q: %w", tool, err)
	}

	endpoint := strings.TrimRight(relay.URL, "/") + "/tool"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request for %q: %w", tool, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+relay.Token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling Omnigent tool %q failed: %w", tool, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOmnigentToolOutputBytes*2))
	if err != nil {
		return "", fmt.Errorf("reading response for Omnigent tool %q failed: %w", tool, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("calling Omnigent tool %q failed with HTTP %d: %s", tool, resp.StatusCode, string(body))
	}

	result := string(body)

	// Unpack standard MCP tool call envelope if present.
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(body, &env); err == nil && (len(env.Content) > 0 || len(env.StructuredContent) > 0 || env.IsError) {
		if env.IsError {
			var errMsgs []string
			for _, c := range env.Content {
				if c.Text != "" {
					errMsgs = append(errMsgs, c.Text)
				}
			}
			if len(errMsgs) > 0 {
				return "", fmt.Errorf("calling Omnigent tool %q failed: %s", tool, strings.Join(errMsgs, "; "))
			}
			return "", fmt.Errorf("calling Omnigent tool %q failed", tool)
		}
		if len(env.StructuredContent) > 0 && string(env.StructuredContent) != "null" {
			result = string(env.StructuredContent)
		} else if len(env.Content) > 0 {
			var texts []string
			for _, c := range env.Content {
				if c.Text != "" {
					texts = append(texts, c.Text)
				}
			}
			if len(texts) > 0 {
				result = strings.Join(texts, "\n")
			}
		}
	}

	if len(result) > maxOmnigentToolOutputBytes {
		result = result[:maxOmnigentToolOutputBytes] + "\n... (truncated)"
	}
	return result, nil
}

// OmnigentLaunchConfig specifies launch configuration for Omnigent sessions.
type OmnigentLaunchConfig struct {
	PreferredHarnesses []string
	PreferredModel     string
	PromptForHarness   bool
}

// DefaultPreferredOmnigentHarnesses defines standard priority of coding harnesses in Omnigent.
var DefaultPreferredOmnigentHarnesses = []string{
	"antigravity-native-ui",
	"opencode-native-ui",
	"claude-native-ui",
	"codex-native-ui",
	"cursor-native-ui",
	"pi-native-ui",
}

// OmnigentAgentEntry describes an agent returned by sys_agent_list.
type OmnigentAgentEntry struct {
	AgentID           string `json:"agent_id"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Harness           string `json:"harness"`
	AvailableOnHost   *bool  `json:"available_on_host,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// OmnigentAgentListResponse mirrors sys_agent_list output.
type OmnigentAgentListResponse struct {
	Builtins      []OmnigentAgentEntry `json:"builtins"`
	SessionAgents []OmnigentAgentEntry `json:"session_agents"`
	LocalConfigs  []OmnigentAgentEntry `json:"local_configs"`
}

// OmnigentAgentChoice describes a candidate coding agent option for the developer.
type OmnigentAgentChoice struct {
	AgentID     string `json:"agentId"`
	DisplayName string `json:"displayName"`
	Harness     string `json:"harness"`
	IsPreferred bool   `json:"isPreferred"`
}

// MaskCassandraInOmnigentAgents removes or masks Cassandra from sys_agent_list output.
func MaskCassandraInOmnigentAgents(raw string) string {
	var resp OmnigentAgentListResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return raw
	}
	var filteredBuiltins []OmnigentAgentEntry
	for _, a := range resp.Builtins {
		if strings.EqualFold(a.Name, "cassandra") || strings.EqualFold(a.Harness, "cassandra") || strings.HasPrefix(strings.ToLower(a.Harness), "acp:cassandra") {
			continue
		}
		filteredBuiltins = append(filteredBuiltins, a)
	}
	resp.Builtins = filteredBuiltins
	out, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return string(out)
}

// FilterEnabledOmnigentAgents filters out Cassandra and unavailable harnesses.
func FilterEnabledOmnigentAgents(agents []OmnigentAgentEntry) []OmnigentAgentEntry {
	var enabled []OmnigentAgentEntry
	for _, a := range agents {
		lowerName := strings.ToLower(a.Name)
		lowerHarness := strings.ToLower(a.Harness)
		if strings.Contains(lowerName, "cassandra") || strings.Contains(lowerHarness, "cassandra") {
			continue
		}
		if a.AvailableOnHost != nil && !*a.AvailableOnHost {
			continue
		}
		if a.UnavailableReason != "" {
			continue
		}
		enabled = append(enabled, a)
	}
	return enabled
}

func agentMatchesPreference(a *OmnigentAgentEntry, pref string) bool {
	prefLower := strings.ToLower(strings.TrimSpace(pref))
	if prefLower == "" {
		return false
	}
	nameLower := strings.ToLower(a.Name)
	harnessLower := strings.ToLower(a.Harness)
	agentIDLower := strings.ToLower(a.AgentID)

	if nameLower == prefLower || harnessLower == prefLower || agentIDLower == prefLower {
		return true
	}
	if strings.Contains(nameLower, prefLower) || strings.Contains(harnessLower, prefLower) {
		return true
	}
	return false
}

// ResolveOmnigentLaunchAgent deterministically picks an enabled coding agent in Omnigent.
func ResolveOmnigentLaunchAgent(agentListRaw string, cfg OmnigentLaunchConfig, requestedAgentID string) (*OmnigentAgentEntry, error) {
	var resp OmnigentAgentListResponse
	if err := json.Unmarshal([]byte(agentListRaw), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse agent list: %w", err)
	}

	enabled := FilterEnabledOmnigentAgents(resp.Builtins)
	if len(enabled) == 0 {
		return nil, errors.New("no enabled coding agent harnesses available in Omnigent")
	}

	// 1. If caller requested a specific valid coding harness (not cassandra), honor it if enabled.
	if requestedAgentID != "" && !strings.EqualFold(requestedAgentID, "cassandra") {
		for _, a := range enabled {
			if strings.EqualFold(a.AgentID, requestedAgentID) || strings.EqualFold(a.Name, requestedAgentID) {
				return &a, nil
			}
		}
	}

	// 2. Priority match against preferred harnesses.
	preferred := cfg.PreferredHarnesses
	if len(preferred) == 0 {
		preferred = DefaultPreferredOmnigentHarnesses
	}

	for _, pref := range preferred {
		for _, a := range enabled {
			if agentMatchesPreference(&a, pref) {
				return &a, nil
			}
		}
	}

	// 3. Ultimate fallback: pick first enabled coding agent.
	return &enabled[0], nil
}

// GetAvailableOmnigentAgentChoices inspects agent list and returns candidate coding choices.
func GetAvailableOmnigentAgentChoices(agentListRaw string, cfg OmnigentLaunchConfig) []OmnigentAgentChoice {
	var resp OmnigentAgentListResponse
	if err := json.Unmarshal([]byte(agentListRaw), &resp); err != nil {
		return nil
	}
	enabled := FilterEnabledOmnigentAgents(resp.Builtins)
	if len(enabled) == 0 {
		return nil
	}

	bestMatch, _ := ResolveOmnigentLaunchAgent(agentListRaw, cfg, "")
	var bestAgentID string
	if bestMatch != nil {
		bestAgentID = bestMatch.AgentID
	}

	var choices []OmnigentAgentChoice
	for _, a := range enabled {
		isPref := false
		if bestAgentID != "" && strings.EqualFold(a.AgentID, bestAgentID) {
			isPref = true
		}
		name := a.Name
		if name == "" {
			name = a.Harness
		}
		choices = append(choices, OmnigentAgentChoice{
			AgentID:     a.AgentID,
			DisplayName: name,
			Harness:     a.Harness,
			IsPreferred: isPref,
		})
	}
	return choices
}

// FormatOmnigentAgentOptionsForDeveloper converts candidate choices to string options for ask_developer.
func FormatOmnigentAgentOptionsForDeveloper(choices []OmnigentAgentChoice) []string {
	var result []string
	for _, c := range choices {
		name := c.DisplayName
		if name == "" {
			name = c.AgentID
		}
		prefix := ""
		if c.IsPreferred {
			prefix = "(Recommended) "
		}
		result = append(result, fmt.Sprintf("%s%s (%s)", prefix, name, c.Harness))
	}
	return result
}

// OmnigentSessionLaunchArgs represents arguments passed to omnigent_session_launch.
type OmnigentSessionLaunchArgs struct {
	Title           string `json:"title"`
	Message         string `json:"message"`
	AgentID         string `json:"agentId,omitempty"`
	Model           string `json:"model,omitempty"`
	Placement       string `json:"placement,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

func registerOmnigentTools(r *Registry) {
	if !IsOmnigentSession() {
		return
	}

	capDef := llm.ToolDef{
		Name:        "omnigent_agent_capabilities",
		Description: "List the available agent harnesses and models in Omnigent to address code review findings. Cassandra is exclusively a read-only code review agent and cannot execute code fixes.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}

	RegisterToolWithArgs(r, capDef, func(ctx context.Context, _ struct{}) (string, error) {
		raw, err := callOmnigentTool(ctx, "sys_agent_list", map[string]any{})
		if err != nil {
			return "", err
		}
		return MaskCassandraInOmnigentAgents(raw), nil
	})

	launchDef := llm.ToolDef{
		Name:        "omnigent_session_launch",
		Description: "Create a new session in Omnigent to carry out tasks, such as addressing code review findings. Use this tool when explicitly asked by the user, or after the user agrees to spawn a fix session.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Title of the session, formatted as 'Review Fix [short description]'.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Initial task instructions for the spawned session, including context, user directions, and technical fix guidelines.",
				},
				"agentId": map[string]any{
					"type":        "string",
					"description": "The agent ID selected from omnigent_agent_capabilities (e.g. 'antigravity-native-ui', 'opencode-native-ui'). Must never be 'cassandra'.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional model override for the spawned session.",
				},
				"placement": map[string]any{
					"type":        "string",
					"description": "Placement for the new session: 'child' (default, nested in current conversation tree), 'same_host' (independent session on current host/workspace), or 'managed' (sandbox).",
					"enum":        []string{"child", "same_host", "managed"},
				},
				"reasoningEffort": map[string]any{
					"type":        "string",
					"description": "Optional reasoning effort for the spawned session (e.g. 'none', 'low', 'medium', 'high', 'max').",
					"enum":        []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
				},
			},
			"required": []string{"title", "message"},
		},
	}

	RegisterToolWithArgs(r, launchDef, func(ctx context.Context, args OmnigentSessionLaunchArgs) (string, error) {
		trimmed := strings.TrimSpace(args.Title)
		if strings.HasPrefix(trimmed, "Review Fix") {
			rest := strings.TrimLeft(strings.TrimPrefix(trimmed, "Review Fix"), " -:")
			if rest != "" {
				args.Title = "Review Fix " + rest
			} else {
				args.Title = "Review Fix"
			}
		} else if trimmed != "" {
			args.Title = "Review Fix " + trimmed
		} else {
			args.Title = "Review Fix"
		}

		agentListRaw, err := callOmnigentTool(ctx, "sys_agent_list", map[string]any{})
		if err != nil {
			return "", fmt.Errorf("failed to query Omnigent agent list: %w", err)
		}

		// Prompt developer if agent selection is missing, prompt-for-harness is true, and prompter is available
		needsPrompt := args.AgentID == "" || strings.EqualFold(args.AgentID, "cassandra")

		if needsPrompt && r.omnigentConfig.PromptForHarness && r.prompter != nil {
			choices := GetAvailableOmnigentAgentChoices(agentListRaw, r.omnigentConfig)
			if len(choices) > 1 {
				formatted := FormatOmnigentAgentOptionsForDeveloper(choices)
				resp, pErr := r.prompter.AskDeveloper(ctx, AskDeveloperRequest{
					Question:      "Which coding agent harness would you like to use for this fix session?",
					Reasoning:     "Cassandra is a read-only review agent. A coding agent harness must be selected to modify files and run tests.",
					Options:       formatted,
					IsMultiSelect: false,
				})
				if pErr == nil && resp.Status == "answered" && len(resp.Selected) > 0 {
					selectedText := resp.Selected[0]
					for _, c := range choices {
						cleanFormatted := strings.TrimPrefix(selectedText, "(Recommended) ")
						name := c.DisplayName
						if name == "" {
							name = c.AgentID
						}
						if strings.HasPrefix(cleanFormatted, name) {
							args.AgentID = c.AgentID
							break
						}
					}
				}
			}
		}

		resolved, rErr := ResolveOmnigentLaunchAgent(agentListRaw, r.omnigentConfig, args.AgentID)
		if rErr != nil {
			return "", fmt.Errorf("failed to resolve coding agent harness: %w", rErr)
		}
		if resolved == nil || resolved.AgentID == "" || strings.EqualFold(resolved.AgentID, "cassandra") || strings.EqualFold(resolved.Name, "cassandra") {
			return "", errors.New("failed to resolve a valid coding agent harness: Cassandra cannot be used to execute fixes")
		}

		// Default placement to "child", but allow overriding if specified
		placement := strings.TrimSpace(args.Placement)
		if placement == "" {
			placement = "child"
		}

		createPayload := map[string]any{
			"agent_id":  resolved.AgentID,
			"title":     args.Title,
			"message":   args.Message,
			"placement": placement,
		}
		if args.Model != "" {
			createPayload["model"] = args.Model
		} else if r.omnigentConfig.PreferredModel != "" {
			createPayload["model"] = r.omnigentConfig.PreferredModel
		}
		if args.ReasoningEffort != "" {
			createPayload["reasoning_effort"] = args.ReasoningEffort
		}

		rawCreateResp, err := callOmnigentTool(ctx, "sys_session_create", createPayload)
		if err != nil {
			return "", fmt.Errorf("failed to create session in Omnigent: %w", err)
		}

		var createResp struct {
			ConversationID string `json:"conversation_id"`
			AgentID        string `json:"agent_id"`
			Title          string `json:"title"`
			Status         string `json:"status"`
		}
		if err := json.Unmarshal([]byte(rawCreateResp), &createResp); err == nil && createResp.ConversationID != "" {
			title := createResp.Title
			if title == "" {
				title = args.Title
			}
			return fmt.Sprintf("Successfully launched fix session [%s](omnigent-session://%s) (agent: %s, conversation ID: %s, status: %s)",
				title, createResp.ConversationID, resolved.Name, createResp.ConversationID, createResp.Status), nil
		}

		return rawCreateResp, nil
	})
}

// GetCurrentSessionTitle returns the title of the current Omnigent session if running in Omnigent.
func GetCurrentSessionTitle(ctx context.Context) (string, error) {
	if !IsOmnigentSession() {
		return "", errors.New("not in an Omnigent session")
	}

	out, err := callOmnigentTool(ctx, "sys_session_get_info", map[string]any{})
	if err != nil {
		return "", err
	}

	var info struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return "", fmt.Errorf("failed to parse sys_session_get_info response: %w", err)
	}

	if info.Title != "" {
		return info.Title, nil
	}

	return "", errors.New("current session title is empty")
}

// UpdateSessionTitlePrefix updates the current Omnigent session title with the specified review status emoji prefix.
// If not in an Omnigent session, it is a no-op and returns nil.
func UpdateSessionTitlePrefix(ctx context.Context, emoji string) error {
	if !IsOmnigentSession() {
		return nil
	}

	currentTitle, err := GetCurrentSessionTitle(ctx)
	if err != nil {
		currentTitle = "Review"
	}

	newTitle := FormatThreadTitle(emoji, currentTitle)
	if newTitle == currentTitle {
		return nil
	}

	_, err = callOmnigentTool(ctx, "sys_session_rename", map[string]any{
		"title": newTitle,
	})
	return err
}
