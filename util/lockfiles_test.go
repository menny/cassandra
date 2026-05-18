package util

import "testing"

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
