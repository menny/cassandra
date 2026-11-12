package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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
	// No CacheBreakpoint → CacheControl must be zero-value (not sent).
	assert.Equal(t, "", string(system[0].CacheControl.Type), "CacheControl.Type should be empty when CacheBreakpoint is false")
}

func TestToAnthropicMessages_SystemWithCacheBreakpoint(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "stable prefix", CacheBreakpoint: true},
		{Role: llm.RoleSystem, Text: "dynamic suffix"},
	}
	system, _, err := toAnthropicMessages(msgs)
	require.NoError(t, err)
	require.Len(t, system, 2)

	// First block: cache_control must be set to "ephemeral".
	assert.Equal(t, "ephemeral", string(system[0].CacheControl.Type),
		"stable block should carry ephemeral cache_control")

	// Second block: no cache_control.
	assert.Equal(t, "", string(system[1].CacheControl.Type),
		"dynamic block should not carry a cache_control marker")
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

func TestParseAnthropicResponse_RecordsStopReasonAndBlockTypes(t *testing.T) {
	raw := `{
		"id": "msg_empty",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "thinking", "thinking": "hmm", "signature": "sig"}],
		"model": "claude-opus-5-5",
		"stop_reason": "refusal",
		"usage": {"input_tokens": 100, "output_tokens": 0}
	}`
	var msg anthropicsdk.Message
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))

	resp, err := parseAnthropicResponse(&msg)
	require.NoError(t, err)
	assert.Empty(t, resp.Text)
	assert.Equal(t, "refusal", resp.RawFinishReason)
	assert.Equal(t, llm.FinishReasonRefusal, resp.FinishReason)
	assert.Equal(t, []string{"thinking"}, resp.ContentTypes)
	assert.Contains(t, resp.Describe(), `stop_reason="refusal"`)
	assert.Contains(t, resp.Describe(), "content_blocks=[thinking]")
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

// ── Bedrock vs Anthropic client construction ──────────────────────────────────

const stubMessagesResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"m",` +
	`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":1,"output_tokens":1}}`

type capturedRequest struct {
	path    string
	headers http.Header
	body    map[string]any
}

// stubMessagesServer records the last request it receives and replies with a
// minimal successful Messages response.
func stubMessagesServer(t *testing.T) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		captured.path = r.URL.EscapedPath()
		captured.headers = r.Header.Clone()
		require.NoError(t, json.Unmarshal(raw, &captured.body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubMessagesResponse))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// isolateAWSEnv points the AWS default chain at fake static credentials and
// blocks every other source (shared files, IMDS, bearer token).
func isolateAWSEnv(t *testing.T, region string) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", region)
	t.Setenv("AWS_DEFAULT_REGION", region)
}

func TestNewBedrock_RoutesToInvokeEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantPath string
	}{
		{"inference profile", "us.anthropic.claude-opus-5-5", "/model/us.anthropic.claude-opus-5-5/invoke"},
		{"id with version suffix", "us.anthropic.claude-haiku-4-5-20251001-v1:0", "/model/us.anthropic.claude-haiku-4-5-20251001-v1%3A0/invoke"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWSEnv(t, "us-east-1")
			server, captured := stubMessagesServer(t)

			p, err := NewBedrock(context.Background(), tt.model, nil, option.WithBaseURL(server.URL))
			require.NoError(t, err)

			resp, err := p.GenerateContent(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hello"}}, nil, 256)
			require.NoError(t, err)
			assert.Equal(t, "hi", resp.Text)

			assert.Equal(t, tt.wantPath, captured.path)
			assert.Equal(t, "bedrock-2023-05-31", captured.body["anthropic_version"])
			assert.NotContains(t, captured.body, "model", "Bedrock takes the model from the URL, not the body")
			assert.NotContains(t, captured.body, "stream")
			assert.EqualValues(t, 256, captured.body["max_tokens"])
			assert.Contains(t, captured.headers.Get("Authorization"), "AWS4-HMAC-SHA256", "request must be SigV4 signed")
			assert.Contains(t, captured.headers.Get("Authorization"), "/us-east-1/bedrock/")
			assert.Empty(t, captured.headers.Get("X-Api-Key"), "no Anthropic API key should be sent to Bedrock")
		})
	}
}

func TestNewBedrock_BearerTokenFromEnv(t *testing.T) {
	isolateAWSEnv(t, "us-east-1")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "tok-123")
	server, captured := stubMessagesServer(t)

	p, err := NewBedrock(context.Background(), "us.anthropic.claude-opus-5-5", nil, option.WithBaseURL(server.URL))
	require.NoError(t, err)
	_, err = p.GenerateContent(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hello"}}, nil, 64)
	require.NoError(t, err)

	assert.Equal(t, "Bearer tok-123", captured.headers.Get("Authorization"))
}

func TestNewBedrock_FailsWithoutRegion(t *testing.T) {
	isolateAWSEnv(t, "")

	_, err := NewBedrock(context.Background(), "us.anthropic.claude-opus-5-5", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AWS_REGION")
}

func TestNew_UsesAnthropicEndpointWithAPIKey(t *testing.T) {
	server, captured := stubMessagesServer(t)

	p := New("sk-test", "claude-opus-5-5", nil, option.WithBaseURL(server.URL))
	_, err := p.GenerateContent(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hello"}}, nil, 64)
	require.NoError(t, err)

	assert.Equal(t, "/v1/messages", captured.path)
	assert.Equal(t, "sk-test", captured.headers.Get("X-Api-Key"))
	assert.Equal(t, "claude-opus-5-5", captured.body["model"])
	assert.NotContains(t, captured.body, "anthropic_version")
}
