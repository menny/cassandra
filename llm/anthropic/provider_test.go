package anthropic

import (
	"encoding/json"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/menny/cassandra/llm"
)

// ── toAnthropicMessages ───────────────────────────────────────────────────────

func TestToAnthropicMessages_System(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "you are a reviewer"},
	}
	system, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	require.Len(t, system, 1)
	assert.Empty(t, params)
}

func TestToAnthropicMessages_UserAndAssistant(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "sys"},
		{Role: llm.RoleUser, Text: "hello"},
		{Role: llm.RoleAssistant, Text: "hi there"},
	}
	system, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, system, 1)
	require.Len(t, params, 2)
	assert.Equal(t, anthropicsdk.MessageParamRoleUser, params[0].Role)
	assert.Equal(t, anthropicsdk.MessageParamRoleAssistant, params[1].Role)
}

func TestToAnthropicMessages_AssistantWithToolCalls(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "tc1", Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
			},
		},
	}
	_, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	assert.Equal(t, anthropicsdk.MessageParamRoleAssistant, params[0].Role)
	require.Len(t, params[0].Content, 1)
	// The single part must be a ToolUseBlockParam wrapped in ContentBlockParamUnion.
	assert.NotNil(t, params[0].Content[0].OfToolUse, "expected OfToolUse to be set")
}

func TestToAnthropicMessages_AssistantWithTextAndToolCalls(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			Text: "I'll read that file for you.",
			ToolCalls: []llm.ToolCall{
				{ID: "tc1", Name: "read_file", Arguments: `{"file_path":"foo.go"}`},
			},
		},
	}
	_, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1)
	assert.Equal(t, anthropicsdk.MessageParamRoleAssistant, params[0].Role)
	// Expect two parts: text block first, then tool use block.
	require.Len(t, params[0].Content, 2)
	assert.NotNil(t, params[0].Content[0].OfText, "first part should be text")
	assert.NotNil(t, params[0].Content[1].OfToolUse, "second part should be tool use")
}

func TestToAnthropicMessages_AssistantEmpty(t *testing.T) {
	// An assistant message with neither Text nor ToolCalls should be silently skipped.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "hello"},
		{Role: llm.RoleAssistant}, // empty
		{Role: llm.RoleUser, Text: "world"},
	}
	_, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, params, 2, "empty assistant message should be dropped")
}

func TestToAnthropicMessages_EmptyToolResults(t *testing.T) {
	// A RoleTool message with no results should be silently dropped.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "hello"},
		{Role: llm.RoleTool}, // empty ToolResults
	}
	_, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, params, 1, "empty tool-results message should be dropped")
}

func TestToAnthropicMessages_MalformedArguments(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "tc1", Name: "read_file", Arguments: `not-json`},
			},
		},
	}
	_, _, err := toAnthropicMessages(msgs)
	assert.Error(t, err, "malformed JSON arguments should surface as an error")
}

func TestToAnthropicMessages_ToolResults(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleTool,
			ToolResults: []llm.ToolResult{
				{ToolCallID: "tc1", Name: "read_file", Content: "file contents"},
				{ToolCallID: "tc2", Name: "glob_files", Content: "a.go\nb.go"},
			},
		},
	}
	_, params, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	require.Len(t, params, 1, "all tool results must be in a single user message")
	assert.Equal(t, anthropicsdk.MessageParamRoleUser, params[0].Role)
	assert.Len(t, params[0].Content, 2)
}

// ── toAnthropicTools ──────────────────────────────────────────────────────────

func TestToAnthropicTools(t *testing.T) {
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
	result := toAnthropicTools(tools)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].OfTool)
	assert.Equal(t, "read_file", result[0].OfTool.Name)
	assert.Equal(t, "Read a file", result[0].OfTool.Description.Value)
}

func TestToAnthropicTools_RequiredFromJSONDecode(t *testing.T) {
	// encoding/json unmarshals arrays as []interface{}, not []string.
	// Verify required is forwarded correctly in that case.
	tools := []llm.ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string"},
				},
				"required": []interface{}{"file_path"},
			},
		},
	}
	result := toAnthropicTools(tools)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].OfTool)
	assert.Equal(t, []string{"file_path"}, result[0].OfTool.InputSchema.Required)
}

// ── parseAnthropicResponse ────────────────────────────────────────────────────

func TestParseAnthropicResponse_TextOnly(t *testing.T) {
	raw := `{
		"id": "msg_1",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "looks good"}],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`
	var msg anthropicsdk.Message
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))

	resp, err := parseAnthropicResponse(&msg)
	require.NoError(t, err)
	assert.Equal(t, "looks good", resp.Text)
	assert.Empty(t, resp.ToolCalls)
}

func TestParseAnthropicResponse_ToolCalls(t *testing.T) {
	raw := `{
		"id": "msg_2",
		"type": "message",
		"role": "assistant",
		"content": [{
			"type": "tool_use",
			"id": "toolu_1",
			"name": "read_file",
			"input": {"file_path": "main.go"}
		}],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 20, "output_tokens": 15}
	}`
	var msg anthropicsdk.Message
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))

	resp, err := parseAnthropicResponse(&msg)
	require.NoError(t, err)
	assert.Empty(t, resp.Text)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "toolu_1", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.Contains(t, resp.ToolCalls[0].Arguments, "main.go")
}

func TestParseAnthropicResponse_MultipleToolCalls(t *testing.T) {
	raw := `{
		"id": "msg_3",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "tool_use", "id": "tc1", "name": "read_file", "input": {"file_path": "a.go"}},
			{"type": "tool_use", "id": "tc2", "name": "glob_files", "input": {"query": ".go"}}
		],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 30, "output_tokens": 20}
	}`
	var msg anthropicsdk.Message
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))

	resp, err := parseAnthropicResponse(&msg)
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "tc1", resp.ToolCalls[0].ID)
	assert.Equal(t, "tc2", resp.ToolCalls[1].ID)
}
