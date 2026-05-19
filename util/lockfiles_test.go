package util

import (
	"strings"
	"testing"
)

func TestIsLockFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"go.sum", true},
		{"backend/go.sum", true},
		{"a/b/c/go.sum", true},
		{"Cargo.lock", true},
		{"main.go", false},
		{"go.sum.bak", false},
		{"my-go.sum", false}, // not a path-separated suffix
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := IsLockFile(c.path, DefaultLockFiles); got != c.want {
				t.Errorf("IsLockFile(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestAppendGitExcludeArgs(t *testing.T) {
	cases := []struct {
		ignored []string
		want    []string
	}{
		{[]string{"go.sum", "package-lock.json"}, []string{":(exclude)*go.sum", ":(exclude)*package-lock.json"}},
		{[]string{"", "go.sum"}, []string{":(exclude)*go.sum"}},
		{[]string{}, []string{}},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.ignored, ","), func(t *testing.T) {
			got := AppendGitExcludeArgs([]string{}, c.ignored)
			if len(got) != len(c.want) {
				t.Fatalf("AppendGitExcludeArgs() = %v, want %v", got, c.want)
			}
			for i, v := range got {
				if v != c.want[i] {
					t.Errorf("AppendGitExcludeArgs()[%d] = %q, want %q", i, v, c.want[i])
				}
			}
		})
	}
}
