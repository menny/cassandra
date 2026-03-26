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
)

// Provider implements llm.Model backed by the Anthropic Messages API.
type Provider struct {
	client    anthropicsdk.Client
	modelName string
}

// New creates a Provider for the given model. Extra SDK options (e.g.
// option.WithBaseURL) can be passed for testing or proxying.
func New(apiKey, modelName string, opts ...option.RequestOption) *Provider {
	allOpts := make([]option.RequestOption, 0, 1+len(opts))
	allOpts = append(allOpts, option.WithAPIKey(apiKey))
	allOpts = append(allOpts, opts...)
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

// toAnthropicMessages splits a []llm.Message into a system-prompt block slice
// and a user/assistant message slice, as required by the Anthropic API.
func toAnthropicMessages(messages []llm.Message) ([]anthropicsdk.TextBlockParam, []anthropicsdk.MessageParam, error) {
	var system []anthropicsdk.TextBlockParam
	var params []anthropicsdk.MessageParam

	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			system = append(system, anthropicsdk.TextBlockParam{Text: m.Text})

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
		schema := anthropicsdk.ToolInputSchemaParam{
			Properties: t.Parameters["properties"],
		}
		// Forward required field so the model knows which parameters are mandatory.
		schema.Required = llm.ParseRequired(t.Parameters["required"])

		tp := anthropicsdk.ToolParam{
			Name:        t.Name,
			Description: param.NewOpt(t.Description),
			InputSchema: schema,
		}
		out = append(out, anthropicsdk.ToolUnionParam{OfTool: &tp})
	}
	return out
}

// parseAnthropicResponse converts an Anthropic *Message to a normalised
// *llm.Response.
func parseAnthropicResponse(msg *anthropicsdk.Message) (*llm.Response, error) {
	resp := &llm.Response{}
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
