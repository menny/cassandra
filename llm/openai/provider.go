// Package openai implements llm.Model using the official OpenAI Go SDK.
package openai

import (
	"context"
	"fmt"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/menny/cassandra/llm"
)

// submitReviewToolName is the synthetic tool the OpenAI provider forces the
// model to call to deliver structured output. The name is a documented
// contract — see DESIGN.md — Structured Feedback Extraction.
// Keep it stable; downstream consumers may match on it.
const submitReviewToolName = "submit_review"

// Provider implements llm.Model backed by the OpenAI Chat Completions API.
type Provider struct {
	client    openaisdk.Client
	modelName string
	options   map[string]any
}

// New creates a Provider for the given model. baseURL overrides the default
// OpenAI API endpoint — pass an empty string to use the official API. This
// allows targeting OpenAI-compatible providers (e.g. Ollama, local LLMs).
// Extra SDK options can be passed for testing or additional configuration.
//
// Note: the options map is currently ignored by this provider but is received
// to maintain architectural parity with other providers (e.g. Google) that
// support model-specific tuning via the configuration file.
func New(apiKey, modelName, baseURL string, options map[string]any, opts ...option.RequestOption) *Provider {
	allOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		allOpts = append(allOpts, option.WithBaseURL(baseURL))
	}
	allOpts = append(allOpts, opts...)
	return &Provider{
		client:    openaisdk.NewClient(allOpts...),
		modelName: modelName,
		options:   options,
	}
}

// GenerateContent sends messages to the OpenAI Chat Completions API and
// returns a normalised llm.Response.
func (p *Provider) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, maxTokens int) (*llm.Response, error) {
	msgs, err := toOpenAIMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("openai: building messages: %w", err)
	}

	sdkParams := openaisdk.ChatCompletionNewParams{
		Model:               openaisdk.ChatModel(p.modelName),
		Messages:            msgs,
		MaxCompletionTokens: openaisdk.Int(int64(maxTokens)),
	}
	if len(tools) > 0 {
		sdkParams.Tools = toOpenAITools(tools)
	}

	resp, err := p.client.Chat.Completions.New(ctx, sdkParams)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	return parseOpenAIResponse(resp)
}

// GenerateStructuredContent sends a request to the OpenAI API with a JSON
// Schema response format to enforce structured output.
func (p *Provider) GenerateStructuredContent(ctx context.Context, messages []llm.Message, schema map[string]any, config llm.StructuredConfig) (*llm.Response, error) {
	modelName, maxTokens := config.Resolve(p.modelName)

	msgs, err := toOpenAIMessages(messages)
	if err != nil {
		return nil, fmt.Errorf("openai: building messages: %w", err)
	}

	sdkParams := openaisdk.ChatCompletionNewParams{
		Model:               openaisdk.ChatModel(modelName),
		Messages:            msgs,
		MaxCompletionTokens: openaisdk.Int(int64(maxTokens)),
		ResponseFormat: openaisdk.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openaisdk.ResponseFormatJSONSchemaParam{
				JSONSchema: openaisdk.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   submitReviewToolName,
					Schema: schema,
					Strict: openaisdk.Bool(false),
				},
			},
		},
	}

	resp, err := p.client.Chat.Completions.New(ctx, sdkParams)
	if err != nil {
		return nil, fmt.Errorf("openai: structured: %w", err)
	}
	return parseOpenAIResponse(resp)
}

// toOpenAIMessages converts []llm.Message to the
// []openaisdk.ChatCompletionMessageParamUnion slice expected by the SDK.
//
// Feature divergence: llm.Message.CacheBreakpoint is intentionally not mapped.
// Unlike Anthropic (which requires explicit cache-control markers), OpenAI
// manages prompt caching automatically without caller-controlled breakpoints.
func toOpenAIMessages(messages []llm.Message) ([]openaisdk.ChatCompletionMessageParamUnion, error) {
	var params []openaisdk.ChatCompletionMessageParamUnion

	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			params = append(params, openaisdk.SystemMessage(m.Text))

		case llm.RoleUser:
			params = append(params, openaisdk.UserMessage(m.Text))

		case llm.RoleAssistant:
			var p openaisdk.ChatCompletionAssistantMessageParam
			var hasContent bool
			if m.Text != "" {
				p.Content.OfString = openaisdk.String(m.Text)
				hasContent = true
			}
			for _, tc := range m.ToolCalls {
				p.ToolCalls = append(p.ToolCalls, openaisdk.ChatCompletionMessageToolCallParam{
					ID: tc.ID,
					Function: openaisdk.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
				hasContent = true
			}
			if !hasContent {
				continue
			}
			params = append(params, openaisdk.ChatCompletionMessageParamUnion{OfAssistant: &p})

		case llm.RoleTool:
			// OpenAI requires each tool result as a separate "tool" role message,
			// unlike Anthropic which bundles them into a single user message.
			for _, tr := range m.ToolResults {
				params = append(params, openaisdk.ToolMessage(tr.Content, tr.ToolCallID))
			}
		}
	}
	return params, nil
}

// toOpenAITools converts []llm.ToolDef to the OpenAI SDK tool slice.
func toOpenAITools(tools []llm.ToolDef) []openaisdk.ChatCompletionToolParam {
	out := make([]openaisdk.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openaisdk.ChatCompletionToolParam{
			Function: openaisdk.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openaisdk.String(t.Description),
				Parameters:  openaisdk.FunctionParameters(t.Parameters),
			},
		})
	}
	return out
}

// parseOpenAIResponse converts an openaisdk.ChatCompletion to a normalised
// *llm.Response.
func parseOpenAIResponse(resp *openaisdk.ChatCompletion) (*llm.Response, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in response")
	}

	result := &llm.Response{Usage: llm.UnknownUsage()}

	if resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 {
		result.Usage.PromptTokens = int(resp.Usage.PromptTokens)
		result.Usage.OutputTokens = int(resp.Usage.CompletionTokens)
		result.Usage.ThinkingTokens = int(resp.Usage.CompletionTokensDetails.ReasoningTokens)
		result.Usage.CachedTokens = int(resp.Usage.PromptTokensDetails.CachedTokens)
	}

	choice := resp.Choices[0]
	switch choice.FinishReason {
	case "stop":
		result.FinishReason = llm.FinishReasonStop
	case "length":
		result.FinishReason = llm.FinishReasonLength
	case "tool_calls":
		// Tool calls often have "tool_calls" finish reason in OpenAI,
		// which we treat as "stop" for the purposes of generating content
		// because the model stopped to yield control.
		result.FinishReason = llm.FinishReasonStop
	default:
		result.FinishReason = llm.FinishReasonOther
	}

	msg := choice.Message
	result.Text = msg.Content
	for _, tc := range msg.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return result, nil
}
