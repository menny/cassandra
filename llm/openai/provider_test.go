package openai

import (
	"encoding/json"
	"testing"

	openaisdk "github.com/openai/openai-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/menny/cassandra/llm"
)

// ── toOpenAIMessages ──────────────────────────────────────────────────────────

func TestToOpenAIMessages_System(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "you are a reviewer"},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	assert.NotNil(t, params[0].OfSystem, "expected OfSystem to be set")
}

func TestToOpenAIMessages_User(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "review this diff"},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	assert.NotNil(t, params[0].OfUser, "expected OfUser to be set")
}

func TestToOpenAIMessages_AssistantTextOnly(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Text: "looks good"},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	require.NotNil(t, params[0].OfAssistant, "expected OfAssistant to be set")
	assert.Equal(t, "looks good", params[0].OfAssistant.Content.OfString.Value)
}

func TestToOpenAIMessages_AssistantWithToolCalls(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "tc1", Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
			},
		},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	require.NotNil(t, params[0].OfAssistant, "expected OfAssistant to be set")
	require.Len(t, params[0].OfAssistant.ToolCalls, 1)
	assert.Equal(t, "tc1", params[0].OfAssistant.ToolCalls[0].ID)
	assert.Equal(t, "read_file", params[0].OfAssistant.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"file_path":"foo.go"}`, params[0].OfAssistant.ToolCalls[0].Function.Arguments)
}

func TestToOpenAIMessages_AssistantWithTextAndToolCalls(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			Text: "I'll read that file for you.",
			ToolCalls: []llm.ToolCall{
				{ID: "tc1", Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
			},
		},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	require.NotNil(t, params[0].OfAssistant)
	assert.Equal(t, "I'll read that file for you.", params[0].OfAssistant.Content.OfString.Value)
	require.Len(t, params[0].OfAssistant.ToolCalls, 1)
}

func TestToOpenAIMessages_AssistantEmpty(t *testing.T) {
	// An assistant message with neither Text nor ToolCalls should be silently skipped.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "hello"},
		{Role: llm.RoleAssistant}, // empty
		{Role: llm.RoleUser, Text: "world"},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, params, 2, "empty assistant message should be dropped")
}

func TestToOpenAIMessages_ToolResults(t *testing.T) {
	// OpenAI requires each tool result as a separate "tool" role message.
	msgs := []llm.Message{
		{
			Role: llm.RoleTool,
			ToolResults: []llm.ToolResult{
				{ToolCallID: "tc1", Name: "read_file", Content: "file contents"},
				{ToolCallID: "tc2", Name: "glob_files", Content: "a.go\nb.go"},
			},
		},
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, params, 2, "each tool result must be its own message")
	for _, p := range params {
		assert.NotNil(t, p.OfTool, "each result message must be OfTool")
	}
}

func TestToOpenAIMessages_EmptyToolResults(t *testing.T) {
	// A RoleTool message with no results should produce no output messages.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "hello"},
		{Role: llm.RoleTool}, // empty ToolResults
	}
	params, err := toOpenAIMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, params, 1, "empty tool-results message should be dropped")
}

// ── toOpenAITools ─────────────────────────────────────────────────────────────

func TestToOpenAITools(t *testing.T) {
	tools := []llm.ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "path"},
				},
				"required": []string{"file_path"},
			},
		},
	}
	result := toOpenAITools(tools)
	require.Len(t, result, 1)
	assert.Equal(t, "read_file", result[0].Function.Name)
	assert.Equal(t, "Read a file", result[0].Function.Description.Value)
	assert.Equal(t, "object", result[0].Function.Parameters["type"])
}

func TestToOpenAITools_Empty(t *testing.T) {
	result := toOpenAITools(nil)
	assert.Empty(t, result)
}

// ── parseOpenAIResponse ───────────────────────────────────────────────────────

func TestParseOpenAIResponse_TextOnly(t *testing.T) {
	raw := `{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"content": "looks good",
				"refusal": null
			},
			"logprobs": null
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	var resp openaisdk.ChatCompletion
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	result, err := parseOpenAIResponse(&resp)
	require.NoError(t, err)
	assert.Equal(t, "looks good", result.Text)
	assert.Empty(t, result.ToolCalls)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 5, result.Usage.OutputTokens)
}

func TestParseOpenAIResponse_ToolCalls(t *testing.T) {
	raw := `{
		"id": "chatcmpl-2",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": null,
				"refusal": null,
				"tool_calls": [{
					"id": "call_abc",
					"type": "function",
					"function": {
						"name": "read_file",
						"arguments": "{\"file_path\":\"main.go\"}"
					}
				}]
			},
			"logprobs": null
		}],
		"usage": {"prompt_tokens": 20, "completion_tokens": 15, "total_tokens": 35}
	}`
	var resp openaisdk.ChatCompletion
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	result, err := parseOpenAIResponse(&resp)
	require.NoError(t, err)
	assert.Empty(t, result.Text)
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, "call_abc", result.ToolCalls[0].ID)
	assert.Equal(t, "read_file", result.ToolCalls[0].Name)
	assert.Contains(t, result.ToolCalls[0].Arguments, "main.go")
}

func TestParseOpenAIResponse_MultipleToolCalls(t *testing.T) {
	raw := `{
		"id": "chatcmpl-3",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": null,
				"refusal": null,
				"tool_calls": [
					{"id": "tc1", "type": "function", "function": {"name": "read_file", "arguments": "{\"file_path\":\"a.go\"}"}},
					{"id": "tc2", "type": "function", "function": {"name": "glob_files", "arguments": "{\"pattern\":\".go\"}"}}
				]
			},
			"logprobs": null
		}],
		"usage": {"prompt_tokens": 30, "completion_tokens": 20, "total_tokens": 50}
	}`
	var resp openaisdk.ChatCompletion
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	result, err := parseOpenAIResponse(&resp)
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 2)
	assert.Equal(t, "tc1", result.ToolCalls[0].ID)
	assert.Equal(t, "tc2", result.ToolCalls[1].ID)
}

func TestParseOpenAIResponse_NoChoices(t *testing.T) {
	raw := `{
		"id": "chatcmpl-4",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [],
		"usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}`
	var resp openaisdk.ChatCompletion
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	_, err := parseOpenAIResponse(&resp)
	assert.Error(t, err, "empty choices should return an error")
}

func TestParseOpenAIResponse_UsageWithCachedAndReasoning(t *testing.T) {
	raw := `{
		"id": "chatcmpl-5",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "o3",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {"role": "assistant", "content": "done", "refusal": null},
			"logprobs": null
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150,
			"prompt_tokens_details": {"cached_tokens": 40},
			"completion_tokens_details": {"reasoning_tokens": 10}
		}
	}`
	var resp openaisdk.ChatCompletion
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	result, err := parseOpenAIResponse(&resp)
	require.NoError(t, err)
	assert.Equal(t, 100, result.Usage.PromptTokens)
	assert.Equal(t, 50, result.Usage.OutputTokens)
	assert.Equal(t, 40, result.Usage.CachedTokens)
	assert.Equal(t, 10, result.Usage.ThinkingTokens)
}
