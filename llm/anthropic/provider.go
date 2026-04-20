// Package anthropic implements llm.Model using the official Anthropic Go SDK.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/internal/util"
)

// submitReviewToolName is the synthetic tool the Anthropic provider forces
// the model to call to deliver structured output. The name is a documented
// contract — see DESIGN.md §Technical Decisions 4 ("Structured Feedback
// Extraction"). Keep it stable; downstream consumers may match on it.
const submitReviewToolName = "submit_review"

// Provider implements llm.Model backed by the Anthropic Messages API.
type Provider struct {
	client    anthropicsdk.Client
	modelName string
}

// New creates a Provider for the given model. Extra SDK options (e.g.
// option.WithBaseURL) can be passed for testing or proxying.
func New(apiKey, modelName string, opts ...option.RequestOption) *Provider {
	allOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &Provider{client: anthropicsdk.NewClient(allOpts...), modelName: modelName}
}

// GenerateContent sends messages to the Anthropic Messages API and returns a
// normalised llm.Response.
func (p *Provider) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, maxTokens int) (*llm.Response, error) {
	systemBlocks, msgParams, err := toAnthropicMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("anthropic: building messages: %w", err)
	}

	sdkParams := anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.Model(p.modelName),
		MaxTokens: int64(maxTokens),
		Messages:  msgParams,
	}
	if len(systemBlocks) > 0 {
		sdkParams.System = systemBlocks
	}
	if len(tools) > 0 {
		sdkParams.Tools = toAnthropicTools(tools)
	}

	resp, err := p.client.Messages.New(ctx, sdkParams)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return parseAnthropicResponse(resp)
}

// GenerateStructuredContent sends a request to the Anthropic API with a single
// tool and forces the model to use it to provide structured output.
func (p *Provider) GenerateStructuredContent(ctx context.Context, messages []llm.Message, schema map[string]any, config llm.StructuredConfig) (*llm.Response, error) {
	systemBlocks, msgParams, err := toAnthropicMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("anthropic: building messages: %w", err)
	}

	modelName, maxTokens := config.Resolve(p.modelName)

	// Define a synthetic tool to enforce the structured response.
	tool := anthropicsdk.ToolParam{
		Name:        submitReviewToolName,
		Description: param.NewOpt("Returns the structured code review."),
		InputSchema: schemaParamFromJSONSchema(schema),
	}

	sdkParams := anthropicsdk.MessageNewParams{
		Model:     anthropicsdk.Model(modelName),
		MaxTokens: int64(maxTokens),
		Messages:  msgParams,
		Tools:     []anthropicsdk.ToolUnionParam{{OfTool: &tool}},
		// Force the model to use our extraction tool.
		ToolChoice: anthropicsdk.ToolChoiceUnionParam{
			OfTool: &anthropicsdk.ToolChoiceToolParam{
				Type: "tool",
				Name: submitReviewToolName,
			},
		},
	}
	if len(systemBlocks) > 0 {
		sdkParams.System = systemBlocks
	}

	resp, err := p.client.Messages.New(ctx, sdkParams)
	if err != nil {
		return nil, fmt.Errorf("anthropic: structured: %w", err)
	}

	normalized, err := parseAnthropicResponse(resp)
	if err != nil {
		return nil, err
	}

	// For forced tool choice, we expect the structured data in the first tool call.
	if len(normalized.ToolCalls) > 0 {
		normalized.Text = normalized.ToolCalls[0].Arguments
	}

	return normalized, nil
}

// toAnthropicMessages splits a []llm.Message into a system-prompt block slice
// and a user/assistant message slice, as required by the Anthropic API.
func toAnthropicMessages(messages []llm.Message) ([]anthropicsdk.TextBlockParam, []anthropicsdk.MessageParam, error) {
	var system []anthropicsdk.TextBlockParam
	var params []anthropicsdk.MessageParam

	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			block := anthropicsdk.TextBlockParam{Text: m.Text}
			if m.CacheBreakpoint {
				block.CacheControl = anthropicsdk.NewCacheControlEphemeralParam()
			}
			system = append(system, block)

		case llm.RoleUser:
			params = append(params, anthropicsdk.NewUserMessage(
				anthropicsdk.NewTextBlock(m.Text),
			))

		case llm.RoleAssistant:
			var parts []anthropicsdk.ContentBlockParamUnion
			if m.Text != "" {
				parts = append(parts, anthropicsdk.NewTextBlock(m.Text))
			}
			for _, tc := range m.ToolCalls {
				var input any
				if err := tc.UnmarshalArguments(&input); err != nil {
					return nil, nil, err
				}
				parts = append(parts, anthropicsdk.ContentBlockParamUnion{
					OfToolUse: &anthropicsdk.ToolUseBlockParam{
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					},
				})
			}
			if len(parts) == 0 {
				continue
			}
			params = append(params, anthropicsdk.NewAssistantMessage(parts...))

		case llm.RoleTool:
			if len(m.ToolResults) == 0 {
				continue
			}
			// All tool results go into a single user message, each as a
			// ToolResultBlockParam, to maintain strict role alternation.
			var parts []anthropicsdk.ContentBlockParamUnion
			for _, tr := range m.ToolResults {
				parts = append(parts, anthropicsdk.NewToolResultBlock(tr.ToolCallID, tr.Content, false))
			}
			params = append(params, anthropicsdk.NewUserMessage(parts...))
		}
	}
	return system, params, nil
}

// toAnthropicTools converts []llm.ToolDef to the Anthropic SDK's ToolUnionParam slice.
func toAnthropicTools(tools []llm.ToolDef) []anthropicsdk.ToolUnionParam {
	out := make([]anthropicsdk.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tp := anthropicsdk.ToolParam{
			Name:        t.Name,
			Description: param.NewOpt(t.Description),
			InputSchema: schemaParamFromJSONSchema(t.Parameters),
		}
		out = append(out, anthropicsdk.ToolUnionParam{OfTool: &tp})
	}
	return out
}

// schemaParamFromJSONSchema converts a JSON Schema object (as stored in
// llm.ToolDef.Parameters) into an Anthropic ToolInputSchemaParam. The
// Anthropic API requires the top-level schema to be an "object"; if a tool
// defines a different top-level type (e.g. "array") the resulting schema
// will be malformed.
func schemaParamFromJSONSchema(schema map[string]any) anthropicsdk.ToolInputSchemaParam {
	return anthropicsdk.ToolInputSchemaParam{
		Type:       "object",
		Properties: schema["properties"],
		Required:   util.ParseRequired(schema["required"]),
	}
}

// parseAnthropicResponse converts an Anthropic *Message to a normalised
// *llm.Response.
func parseAnthropicResponse(msg *anthropicsdk.Message) (*llm.Response, error) {
	resp := &llm.Response{Usage: llm.UnknownUsage()}

	// The SDK usage struct is not a pointer, but we check if we have values.
	// Cache-only responses report tokens exclusively through the cache counters,
	// so the gate must include those or CachedTokens would stay at its -1 sentinel.
	if msg.Usage.InputTokens > 0 || msg.Usage.OutputTokens > 0 ||
		msg.Usage.CacheReadInputTokens > 0 || msg.Usage.CacheCreationInputTokens > 0 {
		resp.Usage.PromptTokens = int(msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens)
		resp.Usage.OutputTokens = int(msg.Usage.OutputTokens)
		resp.Usage.CachedTokens = int(msg.Usage.CacheReadInputTokens)
	}

	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			resp.Text += block.Text
		case "tool_use":
			argsJSON, err := json.Marshal(block.Input)
			if err != nil {
				return nil, fmt.Errorf("anthropic: marshaling tool call %q input: %w", block.Name, err)
			}
			resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(argsJSON),
			})
		}
	}
	return resp, nil
}
