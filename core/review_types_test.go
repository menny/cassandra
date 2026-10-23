package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
			name: "verdict bold prefix with formatting",
			input: `Looks good to me.
**Verdict**: Approved`,
			expected: VerdictApproved,
		},
		{
			name: "verdict bold status with inner formatting",
			input: `Review finished.
Verdict: **Approved**`,
			expected: VerdictApproved,
		},
		{
			name: "verdict bold rejected with formatting",
			input: `Blocker found.
**Verdict**: Rejected`,
			expected: VerdictRejected,
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
		{
			name: "explicit negation with emoji should not approve",
			input: `Critical blockers remain unaddressed.
- ✅ NOT approved`,
			expected: VerdictRejected,
		},
		{
			name: "disapproved with bold header",
			input: `Architecture breaks API contract.
**Verdict**: Disapproved`,
			expected: VerdictRejected,
		},
		{
			name: "unapproved with bullet",
			input: `Please address the comments first.
- Unapproved`,
			expected: VerdictRejected,
		},
		{
			name: "request changes verdict",
			input: `Tests are failing.
**Verdict**: Request Changes`,
			expected: VerdictRejected,
		},
		{
			name: "changes requested verdict",
			input: `Missing regression tests.
Status: Changes Requested`,
			expected: VerdictRejected,
		},
		{
			name: "descriptive prose mentioning approved after fixes does not false-approve",
			input: `Great work on the refactor.
Approved after these 5 critical bugs are fixed.`,
			expected: VerdictElse,
		},
		{
			name:     "descriptive prose saying I have not approved does not false-approve",
			input:    `I have not approved this PR yet because CI is broken.`,
			expected: VerdictElse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetermineVerdict(tt.input))
		})
	}
}
