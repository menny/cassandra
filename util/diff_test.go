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

	t.Run("quoted paths with tabs and special characters", func(t *testing.T) {
		diff := "diff --git \"a/file\\twith\\ttabs.txt\" \"b/file\\twith\\ttabs.txt\"\n" +
			"index 1111111..2222222 100644\n" +
			"--- \"a/file\\twith\\ttabs.txt\"\n" +
			"+++ \"b/file\\twith\\ttabs.txt\"\n" +
			"@@ -1 +1 @@\n" +
			"+hello\n" +
			"diff --git \"a/path/with spaces/file.txt\" \"b/path/with spaces/file.txt\"\n" +
			"index 3333333..4444444 100644\n" +
			"--- \"a/path/with spaces/file.txt\"\n" +
			"+++ \"b/path/with spaces/file.txt\"\n" +
			"@@ -1 +1 @@\n" +
			"+world\n"
		chunks := SplitUnifiedDiff(diff)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(chunks))
		}
		if _, ok := chunks["file\twith\ttabs.txt"]; !ok {
			t.Errorf("missing chunk for 'file\\twith\\ttabs.txt'")
		}
		if _, ok := chunks["path/with spaces/file.txt"]; !ok {
			t.Errorf("missing chunk for 'path/with spaces/file.txt'")
		}
	})

	t.Run("fallback when b prefix is missing or dev null", func(t *testing.T) {
		diff := `diff --git a/deleted_file.txt /dev/null
deleted file mode 100644
--- a/deleted_file.txt
+++ /dev/null
@@ -1 +0,0 @@
-deleted
diff --git a/source_only.txt other_trailing_data
--- a/source_only.txt
+++ /dev/null
@@ -1 +0,0 @@
-only
`
		chunks := SplitUnifiedDiff(diff)
		if _, ok := chunks["deleted_file.txt"]; !ok {
			t.Errorf("expected key deleted_file.txt")
		}
		if _, ok := chunks["source_only.txt"]; !ok {
			t.Errorf("expected key source_only.txt without trailing tokens")
		}
	})
}

func TestDiffMapToSizes(t *testing.T) {
	if got := DiffMapToSizes(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}

	chunks := map[string]string{
		"a.go": "12345",
		"b.go": "1234567890",
	}
	sizes := DiffMapToSizes(chunks)
	if sizes["a.go"] != 5 {
		t.Errorf("expected 5 for a.go, got %d", sizes["a.go"])
	}
	if sizes["b.go"] != 10 {
		t.Errorf("expected 10 for b.go, got %d", sizes["b.go"])
	}
}

func stringsTrimRight(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
