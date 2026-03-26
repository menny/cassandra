// Package google implements llm.Model using the official Google Gen AI Go SDK.
package google

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/genai"

	"github.com/menny/cassandra/llm"
)

// Provider implements llm.Model backed by the Google Generative AI API.
type Provider struct {
	client    *genai.Client
	modelName string
}

// New creates a Provider for the given model using the Gemini Developer API.
func New(ctx context.Context, apiKey, modelName string) (*Provider, error) {
	c, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		return nil, fmt.Errorf("google: failed to create client: %w", err)
	}
	return &Provider{client: c, modelName: modelName}, nil
}

// newWithClient creates a Provider from a pre-configured *genai.Client.
// Intended for testing only.
func newWithClient(client *genai.Client, modelName string) *Provider {
	return &Provider{client: client, modelName: modelName}
}

// GenerateContent sends messages to the Gemini API and returns a normalised
// llm.Response.
func (p *Provider) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, maxTokens int) (*llm.Response, error) {
	contents, systemInstruction := toContents(messages)

	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(maxTokens), //nolint:gosec // bounded by caller
	}
	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}
	if len(tools) > 0 {
		config.Tools = toGenaiTools(tools)
	}

	resp, err := p.client.Models.GenerateContent(ctx, p.modelName, contents, config)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	return parseGenaiResponse(resp)
}

// toContents converts []llm.Message to the []*genai.Content slice expected by
// the SDK, extracting any system-role message as a separate instruction.
func toContents(messages []llm.Message) ([]*genai.Content, *genai.Content) {
	var contents []*genai.Content
	var system *genai.Content

	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			system = &genai.Content{
				Parts: []*genai.Part{{Text: m.Text}},
			}

		case llm.RoleUser:
			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{{Text: m.Text}},
			})

		case llm.RoleAssistant:
			var parts []*genai.Part
			if m.Text != "" {
				parts = append(parts, &genai.Part{Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				if tc.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Arguments), &args)
				}
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						Name: tc.Name,
						Args: args,
					},
				})
			}
			contents = append(contents, &genai.Content{Role: "model", Parts: parts})

		case llm.RoleTool:
			// Each tool result becomes a separate FunctionResponse part inside
			// a single "user" content block.
			var parts []*genai.Part
			for _, tr := range m.ToolResults {
				parts = append(parts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						Name:     tr.Name,
						Response: map[string]any{"result": tr.Content},
					},
				})
			}
			contents = append(contents, &genai.Content{Role: "user", Parts: parts})
		}
	}
	return contents, system
}

// toGenaiTools converts []llm.ToolDef to the Gemini SDK []*genai.Tool slice.
func toGenaiTools(tools []llm.ToolDef) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  convertSchema(t.Parameters),
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// convertSchema recursively converts a JSON Schema map[string]any to a
// *genai.Schema. The Gemini API uses uppercase type names ("OBJECT", "STRING")
// while JSON Schema uses lowercase ("object", "string").
func convertSchema(m map[string]any) *genai.Schema {
	if m == nil {
		return nil
	}
	s := &genai.Schema{}

	if t, ok := m["type"].(string); ok {
		switch t {
		case "object":
			s.Type = genai.TypeObject
		case "string":
			s.Type = genai.TypeString
		case "number":
			s.Type = genai.TypeNumber
		case "integer":
			s.Type = genai.TypeInteger
		case "boolean":
			s.Type = genai.TypeBoolean
		case "array":
			s.Type = genai.TypeArray
		}
	}
	if desc, ok := m["description"].(string); ok {
		s.Description = desc
	}
	if props, ok := m["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for k, v := range props {
			if vm, ok := v.(map[string]any); ok {
				s.Properties[k] = convertSchema(vm)
			}
		}
	}
	// JSON unmarshalling may produce []interface{} for "required".
	switch req := m["required"].(type) {
	case []string:
		s.Required = req
	case []interface{}:
		for _, r := range req {
			if rs, ok := r.(string); ok {
				s.Required = append(s.Required, rs)
			}
		}
	}
	return s
}

// parseGenaiResponse converts a *genai.GenerateContentResponse to a normalised
// *llm.Response.
func parseGenaiResponse(resp *genai.GenerateContentResponse) (*llm.Response, error) {
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("google: no candidates in response")
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil {
		return nil, fmt.Errorf("google: candidate has no content")
	}

	result := &llm.Response{}
	for _, part := range candidate.Content.Parts {
		switch {
		case part.Text != "":
			result.Text += part.Text
		case part.FunctionCall != nil:
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				// Gemini does not provide a stable call ID in function calls;
				// use the function name as a fallback identifier.
				ID:        part.FunctionCall.Name,
				Name:      part.FunctionCall.Name,
				Arguments: string(argsJSON),
			})
		}
	}
	return result, nil
}
