// Package llm defines the provider-agnostic interface and types used throughout
// Cassandra. No package outside llm/ should import a provider sub-package
// directly; they interact exclusively through the Model interface defined here.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// Role identifies the author of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single turn in a conversation.
// Fields are zero-valued when not applicable to the Role:
//   - RoleSystem / RoleUser / RoleAssistant (text-only): only Text is set.
//   - RoleAssistant (tool requests): ToolCalls is set (Text may also be set).
//   - RoleTool: ToolResults is set.
type Message struct {
	Role             Role
	Text             string
	ToolCalls        []ToolCall
	ToolResults      []ToolResult
	Reasoning        string         // Internal reasoning/thought process from the model
	ProviderMetadata map[string]any // Opaque provider-specific data (e.g. thought signatures)
	// CacheBreakpoint, when true on a RoleSystem message, marks the end of the
	// stable cacheable prefix. Providers that support prompt caching (e.g.
	// Anthropic) use this to inject a cache-control marker; all other providers
	// ignore it.
	CacheBreakpoint bool
}

// ToolCall is a tool invocation requested by the model in an assistant turn.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON
}

// UnmarshalArguments unmarshals the raw JSON Arguments into the given destination.
// It returns a formatted error if the unmarshaling fails.
func (tc *ToolCall) UnmarshalArguments(dest any) error {
	if tc.Arguments == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(tc.Arguments), dest); err != nil {
		return fmt.Errorf("tool call %q has malformed arguments: %w", tc.Name, err)
	}
	return nil
}

// ToolResult is the response to a ToolCall, bundled into a RoleTool message.
type ToolResult struct {
	ToolCallID string
	Name       string
	Content    string
}

// ToolDef describes a tool the model may call.
// Parameters is a JSON Schema object (same shape accepted by all providers).
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any // full JSON Schema object
}

// Usage captures the number of tokens consumed in an interaction.
// If a provider does not support a specific count, its value will be 0.
// If the provider does not support token counting at all, all fields will be -1.
type Usage struct {
	PromptTokens   int // tokens in the input prompt
	OutputTokens   int // tokens in the generated response (excluding thinking)
	ThinkingTokens int // tokens used for model internal reasoning/thinking
	CachedTokens   int // tokens served from a cache
}

// UnknownUsage returns a Usage with PromptTokens and OutputTokens set to -1,
// indicating the provider did not report any token counts. ThinkingTokens
// and CachedTokens remain 0 (the zero value) which providers overwrite when
// they do have data.
func UnknownUsage() Usage {
	return Usage{PromptTokens: -1, OutputTokens: -1}
}

// Add accumulates other's token counts into u, ignoring sentinel fields
// (values <= 0). Intended for callers that sum per-iteration Usage into a
// running session total without letting UnknownUsage() sentinels
// corrupt the aggregate.
func (u *Usage) Add(other Usage) {
	if other.PromptTokens > 0 {
		u.PromptTokens += other.PromptTokens
	}
	if other.OutputTokens > 0 {
		u.OutputTokens += other.OutputTokens
	}
	if other.ThinkingTokens > 0 {
		u.ThinkingTokens += other.ThinkingTokens
	}
	if other.CachedTokens > 0 {
		u.CachedTokens += other.CachedTokens
	}
}

// TotalInput returns the total number of input-side tokens (prompt + cached).
func (u Usage) TotalInput() int {
	if u.PromptTokens < 0 {
		return -1
	}
	return u.PromptTokens + u.CachedTokens
}

// TotalOutput returns the total number of output-side tokens (output + thinking).
func (u Usage) TotalOutput() int {
	if u.OutputTokens < 0 {
		return -1
	}
	return u.OutputTokens + u.ThinkingTokens
}

// Response is what the model returns from a single GenerateContent call.
// At least one of Text or ToolCalls will be non-empty; providers that support
// mixed streaming turns may populate both simultaneously.
type Response struct {
	Text             string         // set when the model produced a final answer
	ToolCalls        []ToolCall     // set when the model wants to invoke tools
	Reasoning        string         // set when the model provides internal reasoning
	ProviderMetadata map[string]any // opaque data to be echoed in subsequent turns
	Usage            Usage          // token usage for this interaction
}

// DefaultStructuredMaxTokens is the fallback max-tokens budget used by
// providers when StructuredConfig.MaxTokens is unset. Kept in sync with the
// CLI's --max-tokens default (see cmd/ai_reviewer) so the structured
// extraction pass has a consistent headroom across provider implementations.
const DefaultStructuredMaxTokens = 8192

// StructuredConfig provides options for structured output generation.
type StructuredConfig struct {
	// ModelOverride allows using a different model for the structured pass.
	ModelOverride string
	// MaxTokens limits the length of the LLM response.
	MaxTokens int
}

// Resolve returns the effective model and max-tokens values, applying the
// provider's default model when no override is set and
// DefaultStructuredMaxTokens when MaxTokens is non-positive.
func (c StructuredConfig) Resolve(defaultModel string) (string, int) {
	model := defaultModel
	if c.ModelOverride != "" {
		model = c.ModelOverride
	}
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultStructuredMaxTokens
	}
	return model, maxTokens
}

// Model is the only interface core.Agent depends on.
// Implementations live in llm/anthropic and llm/google.
type Model interface {
	GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, maxTokens int) (*Response, error)
	// GenerateStructuredContent requests the model to produce output adhering to
	// the provided JSON Schema. The schema should be a map[string]any following
	// the JSON Schema specification.
	GenerateStructuredContent(ctx context.Context, messages []Message, schema map[string]any, config StructuredConfig) (*Response, error)
}
