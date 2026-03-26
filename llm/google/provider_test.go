package google

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/menny/cassandra/llm"
)

// ── toContents ────────────────────────────────────────────────────────────────

func TestToContents_System(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "you are a reviewer"},
	}
	contents, system := toContents(msgs)
	assert.Empty(t, contents)
	require.NotNil(t, system)
	require.Len(t, system.Parts, 1)
	assert.Equal(t, "you are a reviewer", system.Parts[0].Text)
}

func TestToContents_UserMessage(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "review this diff"},
	}
	contents, system := toContents(msgs)
	assert.Nil(t, system)
	require.Len(t, contents, 1)
	assert.Equal(t, "user", contents[0].Role)
	require.Len(t, contents[0].Parts, 1)
	assert.Equal(t, "review this diff", contents[0].Parts[0].Text)
}

func TestToContents_AssistantWithToolCalls(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "read_file", Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
			},
		},
	}
	contents, _ := toContents(msgs)
	require.Len(t, contents, 1)
	assert.Equal(t, "model", contents[0].Role)
	require.Len(t, contents[0].Parts, 1)
	require.NotNil(t, contents[0].Parts[0].FunctionCall)
	assert.Equal(t, "read_file", contents[0].Parts[0].FunctionCall.Name)
	assert.Equal(t, "foo.go", contents[0].Parts[0].FunctionCall.Args["file_path"])
}

func TestToContents_ToolResults(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleTool,
			ToolResults: []llm.ToolResult{
				{ToolCallID: "read_file", Name: "read_file", Content: "package main"},
				{ToolCallID: "glob_files", Name: "glob_files", Content: "a.go\nb.go"},
			},
		},
	}
	contents, _ := toContents(msgs)
	require.Len(t, contents, 1, "all tool results go into a single content block")
	assert.Equal(t, "user", contents[0].Role)
	require.Len(t, contents[0].Parts, 2)
	assert.NotNil(t, contents[0].Parts[0].FunctionResponse)
	assert.Equal(t, "read_file", contents[0].Parts[0].FunctionResponse.Name)
}

// ── convertSchema ─────────────────────────────────────────────────────────────

func TestConvertSchema_SimpleObject(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "path to the file",
			},
		},
		"required": []interface{}{"file_path"},
	}
	s := convertSchema(input)
	require.NotNil(t, s)
	assert.Equal(t, genai.TypeObject, s.Type)
	require.Contains(t, s.Properties, "file_path")
	assert.Equal(t, genai.TypeString, s.Properties["file_path"].Type)
	assert.Equal(t, "path to the file", s.Properties["file_path"].Description)
	assert.Equal(t, []string{"file_path"}, s.Required)
}

func TestConvertSchema_TypeMapping(t *testing.T) {
	cases := []struct {
		jsonType string
		wantType genai.Type
	}{
		{"object", genai.TypeObject},
		{"string", genai.TypeString},
		{"number", genai.TypeNumber},
		{"integer", genai.TypeInteger},
		{"boolean", genai.TypeBoolean},
		{"array", genai.TypeArray},
	}
	for _, tc := range cases {
		s := convertSchema(map[string]any{"type": tc.jsonType})
		assert.Equal(t, tc.wantType, s.Type, "type %q", tc.jsonType)
	}
}

func TestConvertSchema_Nil(t *testing.T) {
	assert.Nil(t, convertSchema(nil))
}

// ── parseGenaiResponse ────────────────────────────────────────────────────────

func TestParseGenaiResponse_TextOnly(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "looks good"}},
				},
			},
		},
	}
	result, err := parseGenaiResponse(resp)
	require.NoError(t, err)
	assert.Equal(t, "looks good", result.Text)
	assert.Empty(t, result.ToolCalls)
}

func TestParseGenaiResponse_ToolCall(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{
							FunctionCall: &genai.FunctionCall{
								Name: "read_file",
								Args: map[string]any{"file_path": "main.go"},
							},
						},
					},
				},
			},
		},
	}
	result, err := parseGenaiResponse(resp)
	require.NoError(t, err)
	assert.Empty(t, result.Text)
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, "read_file_0", result.ToolCalls[0].ID)
	assert.Equal(t, "read_file", result.ToolCalls[0].Name)
	assert.Contains(t, result.ToolCalls[0].Arguments, "main.go")
}

func TestParseGenaiResponse_NoCandidates(t *testing.T) {
	resp := &genai.GenerateContentResponse{}
	_, err := parseGenaiResponse(resp)
	assert.Error(t, err)
}
