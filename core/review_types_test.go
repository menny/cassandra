package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFileReview_ParseLines(t *testing.T) {
	tests := []struct {
		name      string
		lines     string
		wantStart int
		wantEnd   int
		wantErr   bool
	}{
		{
			name:      "empty string",
			lines:     "",
			wantStart: 0,
			wantEnd:   0,
			wantErr:   false,
		},
		{
			name:      "single line",
			lines:     "42",
			wantStart: 42,
			wantEnd:   42,
			wantErr:   false,
		},
		{
			name:      "single line with whitespace",
			lines:     " 42 ",
			wantStart: 42,
			wantEnd:   42,
			wantErr:   false,
		},
		{
			name:      "valid range",
			lines:     "10-20",
			wantStart: 10,
			wantEnd:   20,
			wantErr:   false,
		},
		{
			name:      "valid range with whitespace",
			lines:     " 10 - 20 ",
			wantStart: 10,
			wantEnd:   20,
			wantErr:   false,
		},
		{
			name:      "inverted range",
			lines:     "20-10",
			wantStart: 10,
			wantEnd:   20,
			wantErr:   false,
		},
		{
			name:    "invalid single line",
			lines:   "abc",
			wantErr: true,
		},
		{
			name:    "invalid range start",
			lines:   "abc-20",
			wantErr: true,
		},
		{
			name:    "invalid range end",
			lines:   "10-abc",
			wantErr: true,
		},
		{
			name:    "too many hyphens",
			lines:   "10-20-30",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &FileReview{Lines: tt.lines}
			gotStart, gotEnd, err := fr.ParseLines()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantStart, gotStart)
				assert.Equal(t, tt.wantEnd, gotEnd)
			}
		})
	}
}
