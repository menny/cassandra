package util

import (
	"testing"
)

func TestSplitUnifiedDiff(t *testing.T) {
	t.Run("empty or whitespace", func(t *testing.T) {
		if got := SplitUnifiedDiff(""); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
		if got := SplitUnifiedDiff("   \n\n"); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("single file", func(t *testing.T) {
		diff := `diff --git a/pkg/foo.go b/pkg/foo.go
index 1234567..89abcdef 100644
--- a/pkg/foo.go
+++ b/pkg/foo.go
@@ -1,3 +1,4 @@
 package pkg
+// added line
 func Foo() {}
`
		chunks := SplitUnifiedDiff(diff)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		content, ok := chunks["pkg/foo.go"]
		if !ok {
			t.Fatalf("expected key pkg/foo.go")
		}
		if content != stringsTrimRight(diff) {
			t.Errorf("content mismatch: got %q, want %q", content, diff)
		}
	})

	t.Run("multiple files with spaces and renames", func(t *testing.T) {
		diff := `diff --git a/pkg/file one.go b/pkg/file one.go
index 1111111..2222222 100644
--- a/pkg/file one.go
+++ b/pkg/file one.go
@@ -1 +1 @@
-old
+new
diff --git a/pkg/old.go b/pkg/renamed.go
similarity index 90%
rename from pkg/old.go
rename to pkg/renamed.go
index 3333333..4444444 100644
--- a/pkg/old.go
+++ b/pkg/renamed.go
@@ -1 +1 @@
-old text
+renamed text
`
		chunks := SplitUnifiedDiff(diff)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(chunks))
		}
		if _, ok := chunks["pkg/file one.go"]; !ok {
			t.Errorf("missing chunk for 'pkg/file one.go'")
		}
		if _, ok := chunks["pkg/renamed.go"]; !ok {
			t.Errorf("missing chunk for 'pkg/renamed.go'")
		}

		sizes := GetFileDiffSizes(diff)
		if len(sizes) != 2 {
			t.Fatalf("expected 2 sizes, got %d", len(sizes))
		}
		if sizes["pkg/file one.go"] <= 0 {
			t.Errorf("expected positive size for pkg/file one.go, got %d", sizes["pkg/file one.go"])
		}
	})
}

func stringsTrimRight(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
