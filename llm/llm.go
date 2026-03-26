// Package llm defines the provider-agnostic interface and types used throughout
// Cassandra. No package outside llm/ should import a provider sub-package
// directly; they interact exclusively through the Model interface defined here.
package llm

import "context"

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
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// ToolCall is a tool invocation requested by the model in an assistant turn.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON
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

// Response is what the model returns from a single GenerateContent call.
// Exactly one of Text or ToolCalls will be non-empty.
type Response struct {
	Text      string     // set when the model produced a final answer
	ToolCalls []ToolCall // set when the model wants to invoke tools
}

// Model is the only interface core.Agent depends on.
// Implementations live in llm/anthropic and llm/google.
type Model interface {
	GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, maxTokens int) (*Response, error)
}

// Provider identifies a supported LLM provider.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderGoogle    Provider = "google"
)
