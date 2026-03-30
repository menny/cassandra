package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
