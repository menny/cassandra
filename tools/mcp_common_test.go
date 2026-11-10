package tools

import (
	"strings"
	"testing"
)

func TestUnpackMCPTextEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		toolDesc  string
		want      string
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "plain text body",
			body:     "raw output from cli",
			toolDesc: "test_tool",
			want:     "raw output from cli",
		},
		{
			name:     "standard text content envelope",
			body:     `{"content": [{"type": "text", "text": "result line 1"}, {"type": "text", "text": "result line 2"}]}`,
			toolDesc: "test_tool",
			want:     "result line 1\nresult line 2",
		},
		{
			name:     "structured content envelope",
			body:     `{"structuredContent": {"status": "ok", "count": 42}}`,
			toolDesc: "test_tool",
			want:     `{"status": "ok", "count": 42}`,
		},
		{
			name:      "error envelope with messages",
			body:      `{"isError": true, "content": [{"type": "text", "text": "file not found"}, {"type": "text", "text": "access denied"}]}`,
			toolDesc:  "test_tool",
			wantErr:   true,
			errSubstr: "file not found; access denied",
		},
		{
			name:      "error envelope without messages",
			body:      `{"isError": true}`,
			toolDesc:  "test_tool",
			wantErr:   true,
			errSubstr: "calling test_tool failed",
		},
		{
			name:     "empty json object",
			body:     `{}`,
			toolDesc: "test_tool",
			want:     `{}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := unpackMCPTextEnvelope([]byte(tc.body), tc.toolDesc)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unpackMCPTextEnvelope() error = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeReviewFixTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", "Review Fix"},
		{"   ", "Review Fix"},
		{"Review Fix", "Review Fix"},
		{"Review Fix:", "Review Fix"},
		{"Review Fix -", "Review Fix"},
		{"Review Fix: add nil check", "Review Fix add nil check"},
		{"Review Fix - add nil check", "Review Fix add nil check"},
		{"Review Fix add nil check", "Review Fix add nil check"},
		{"add nil check", "Review Fix add nil check"},
		{"PR #123", "Review Fix PR #123"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeReviewFixTitle(tc.input)
			if got != tc.want {
				t.Errorf("normalizeReviewFixTitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
