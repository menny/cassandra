package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseRequired(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected []string
	}{
		{
			name:     "string slice",
			input:    []string{"a", "b"},
			expected: []string{"a", "b"},
		},
		{
			name:     "interface slice of strings",
			input:    []interface{}{"c", "d"},
			expected: []string{"c", "d"},
		},
		{
			name:     "interface slice with mixed types",
			input:    []interface{}{"e", 123, "f"},
			expected: []string{"e", "f"},
		},
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "unsupported type",
			input:    "not a slice",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseRequired(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
