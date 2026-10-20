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

func TestDetermineVerdict(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ReviewVerdict
	}{
		{
			name:     "empty review",
			input:    "",
			expected: VerdictElse,
		},
		{
			name: "explicit approved with emoji",
			input: `Here is the review.
Great job on this implementation!
- ✅ Approved`,
			expected: VerdictApproved,
		},
		{
			name: "approved without bullet point",
			input: `Looks great!
✅ Approved`,
			expected: VerdictApproved,
		},
		{
			name: "bold approved",
			input: `Looks good to me.
**Approved**`,
			expected: VerdictApproved,
		},
		{
			name: "explicit rejected with red x",
			input: `There is a security vulnerability in line 42.
- ❌ Rejected`,
			expected: VerdictRejected,
		},
		{
			name: "explicit rejected with green box x",
			input: `Critical bug must be resolved.
- ❎ Rejected`,
			expected: VerdictRejected,
		},
		{
			name: "bold rejected",
			input: `Major design issues found.
**Rejected**`,
			expected: VerdictRejected,
		},
		{
			name: "explicit comment with emoji",
			input: `Non-blocking observations only.
- 💬 Comment`,
			expected: VerdictElse,
		},
		{
			name: "bold comment",
			input: `Just some thoughts.
**Comment**`,
			expected: VerdictElse,
		},
		{
			name: "historical mention earlier does not override closing rejection",
			input: `This feature was previously approved in PR 42.
However, recent changes break the build.
- ❌ Rejected`,
			expected: VerdictRejected,
		},
		{
			name: "historical rejection earlier does not override closing approval",
			input: `I previously rejected this approach, but after your changes it works well.
- ✅ Approved`,
			expected: VerdictApproved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineVerdict(tt.input))
		})
	}
}
