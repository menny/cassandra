package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsage_Add(t *testing.T) {
	t.Run("accumulates positive counts", func(t *testing.T) {
		u := Usage{PromptTokens: 10, OutputTokens: 5}
		u.Add(Usage{PromptTokens: 3, OutputTokens: 2, ThinkingTokens: 7, CachedTokens: 1})
		assert.Equal(t, Usage{PromptTokens: 13, OutputTokens: 7, ThinkingTokens: 7, CachedTokens: 1}, u)
	})

	t.Run("ignores UnknownUsage sentinel", func(t *testing.T) {
		u := Usage{PromptTokens: 10, OutputTokens: 5}
		u.Add(UnknownUsage())
		assert.Equal(t, Usage{PromptTokens: 10, OutputTokens: 5}, u)
	})

	t.Run("ignores zero fields", func(t *testing.T) {
		u := Usage{PromptTokens: 10}
		u.Add(Usage{PromptTokens: 0, OutputTokens: 0, ThinkingTokens: 4})
		assert.Equal(t, Usage{PromptTokens: 10, ThinkingTokens: 4}, u)
	})
}

func TestToolCall_UnmarshalArguments(t *testing.T) {
	t.Run("empty arguments", func(t *testing.T) {
		tc := &ToolCall{Arguments: ""}
		var dest map[string]any
		err := tc.UnmarshalArguments(&dest)
		require.NoError(t, err)
		assert.Nil(t, dest)
	})

	t.Run("valid JSON", func(t *testing.T) {
		tc := &ToolCall{
			Name:      "test_tool",
			Arguments: `{"key": "value", "num": 123}`,
		}
		var dest map[string]any
		err := tc.UnmarshalArguments(&dest)
		require.NoError(t, err)
		require.NotNil(t, dest)
		assert.Equal(t, "value", dest["key"])
		assert.Equal(t, float64(123), dest["num"])
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tc := &ToolCall{
			Name:      "test_tool",
			Arguments: `not-json`,
		}
		var dest map[string]any
		err := tc.UnmarshalArguments(&dest)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `tool call "test_tool" has malformed arguments`)
	})

	t.Run("into struct", func(t *testing.T) {
		tc := &ToolCall{
			Name:      "struct_tool",
			Arguments: `{"field": "hello"}`,
		}

		var dest struct {
			Field string `json:"field"`
		}
		err := tc.UnmarshalArguments(&dest)
		require.NoError(t, err)
		assert.Equal(t, "hello", dest.Field)
	})
}
