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

const (
	maxOmnigentToolOutputBytes = 40 * 1024
	maxRelayConfigFileBytes    = 64 * 1024
)

// omnigentHTTPClient is used for calling Omnigent tool relay HTTP endpoints.
// A bounded timeout and disabled keep-alives prevent leaked background connections
// and unbounded hangs if the relay process becomes unresponsive.
var omnigentHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DisableKeepAlives: true,
	},
}

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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxRelayConfigFileBytes))
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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxRelayConfigFileBytes))
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
	type fileCandidate struct {
		path    string
		modTime time.Time
	}

	collectFiles := func(pattern string) []fileCandidate {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil
		}
		var list []fileCandidate
		for _, m := range matches {
			info, err := os.Stat(m)
			if err == nil {
				list = append(list, fileCandidate{path: m, modTime: info.ModTime()})
			}
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].modTime.After(list[j].modTime)
		})
		return list
	}

	jsonFiles := collectFiles(filepath.Join(omnigentRoot, "*", "*", "tool_relay.json"))

	var untaggedFallback *omnigentRelayConfig

	for _, fc := range jsonFiles {
		cfg, err := readRelayConfigFile(fc.path)
		if err != nil {
			continue
		}
		if targetSessionID != "" {
			if cfg.SessionID == targetSessionID {
				return cfg
			}
			// Keep the newest untagged candidate as fallback if target session ID is not found.
			if cfg.SessionID == "" && untaggedFallback == nil {
				untaggedFallback = cfg
			}
		} else {
			return cfg
		}
	}

	if targetSessionID != "" && untaggedFallback != nil {
		return untaggedFallback
	}

	// Fallback to checking tool_relay.env, sorted by modTime (newest first).
	envFiles := collectFiles(filepath.Join(omnigentRoot, "*", "*", "tool_relay.env"))
	for _, ef := range envFiles {
		cfg, err := readRelayEnvFile(ef.path)
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

	resp, err := omnigentHTTPClient.Do(httpReq)
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

	result, err := unpackMCPTextEnvelope(body, fmt.Sprintf("Omnigent tool %q", tool))
	if err != nil {
		return "", err
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

func maskCassandraInAgentSlice(agents []OmnigentAgentEntry) []OmnigentAgentEntry {
	var filtered []OmnigentAgentEntry
	for _, a := range agents {
		if strings.EqualFold(a.Name, "cassandra") || strings.EqualFold(a.Harness, "cassandra") || strings.HasPrefix(strings.ToLower(a.Harness), "acp:cassandra") {
			continue
		}
		filtered = append(filtered, a)
	}
	return filtered
}

// MaskCassandraInOmnigentAgents removes or masks Cassandra from sys_agent_list output.
func MaskCassandraInOmnigentAgents(raw string) string {
	var resp OmnigentAgentListResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return raw
	}
	resp.Builtins = maskCassandraInAgentSlice(resp.Builtins)
	resp.SessionAgents = maskCassandraInAgentSlice(resp.SessionAgents)
	resp.LocalConfigs = maskCassandraInAgentSlice(resp.LocalConfigs)
	out, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return string(out)
}

// allOmnigentAgents aggregates agents across LocalConfigs, SessionAgents, and Builtins.
// LocalConfigs take precedence over SessionAgents, which take precedence over Builtins.
func allOmnigentAgents(resp *OmnigentAgentListResponse) []OmnigentAgentEntry {
	seen := make(map[string]bool)
	var all []OmnigentAgentEntry

	addAgents := func(entries []OmnigentAgentEntry) {
		for _, a := range entries {
			key := a.AgentID
			if key == "" {
				key = a.Name
			}
			keyLower := strings.ToLower(key)
			if keyLower != "" && seen[keyLower] {
				continue
			}
			if keyLower != "" {
				seen[keyLower] = true
			}
			all = append(all, a)
		}
	}

	addAgents(resp.LocalConfigs)
	addAgents(resp.SessionAgents)
	addAgents(resp.Builtins)
	return all
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

	enabled := FilterEnabledOmnigentAgents(allOmnigentAgents(&resp))
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
	enabled := FilterEnabledOmnigentAgents(allOmnigentAgents(&resp))
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
		args.Title = normalizeReviewFixTitle(args.Title)

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
					cleanFormatted := strings.TrimSpace(strings.TrimPrefix(selectedText, "(Recommended) "))
					for _, c := range choices {
						name := c.DisplayName
						if name == "" {
							name = c.AgentID
						}
						expectedFormatted := fmt.Sprintf("%s (%s)", name, c.Harness)
						if cleanFormatted == expectedFormatted || cleanFormatted == name {
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
