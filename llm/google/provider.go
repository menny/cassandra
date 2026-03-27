// Package google implements llm.Model using the official Google Gen AI Go SDK.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"google.golang.org/genai"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/internal/util"
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

// GenerateContent sends messages to the Gemini API and returns a normalised
// llm.Response.
func (p *Provider) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, maxTokens int) (*llm.Response, error) {
	contents, systemInstruction, err := toContents(messages)
	if err != nil {
		return nil, fmt.Errorf("google: building contents: %w", err)
	}

	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(min(maxTokens, math.MaxInt32)), //nolint:gosec // clamped above
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
func toContents(messages []llm.Message) ([]*genai.Content, *genai.Content, error) {
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
				if err := tc.UnmarshalArguments(&args); err != nil {
					return nil, nil, err
				}
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						Name: tc.Name,
						Args: args,
					},
				})
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, &genai.Content{Role: "model", Parts: parts})

		case llm.RoleTool:
			if len(m.ToolResults) == 0 {
				continue
			}
			// Each tool result becomes a separate FunctionResponse part inside
			// a single "user" content block.
			// The Gemini SDK requires the response to be a map; we wrap the
			// plain-text content under a "result" key by convention.
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
	return contents, system, nil
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
			// TODO: warn on unexpected property shape (expected map[string]any) if !ok
		}
	}

	s.Required = util.ParseRequired(m["required"])

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
		// Both fields are checked independently: the Gemini API can return a
		// part that carries both text and a function call in a mixed turn.
		if part.Text != "" {
			result.Text += part.Text
		}
		if part.FunctionCall != nil {
			argsJSON, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				return nil, fmt.Errorf("google: marshaling tool call %q args: %w", part.FunctionCall.Name, err)
			}
			// Gemini does not provide a stable per-call ID. Append the
			// zero-based index so that two calls to the same tool in one
			// turn produce distinct IDs (e.g. "read_file_0", "read_file_1").
			id := fmt.Sprintf("%s_%d", part.FunctionCall.Name, len(result.ToolCalls))
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				ID:        id,
				Name:      part.FunctionCall.Name,
				Arguments: string(argsJSON),
			})
		}
	}
	return result, nil
}
