package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSandbox(t *testing.T) {
	ctx := context.Background()

	// 1. Setup base directory
	baseDir := t.TempDir()
	err := os.WriteFile(filepath.Join(baseDir, "hello.txt"), []byte("hello world\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Define diff
	diff := `--- hello.txt
+++ hello.txt
@@ -1 +1 @@
-hello world
+hello cassandra
`

	// 3. Create sandbox
	s, err := NewSandbox(ctx, baseDir, diff)
	if err != nil {
		t.Fatalf("NewSandbox failed: %v", err)
	}
	defer s.Cleanup()

	// 4. Verify file content
	content, err := os.ReadFile(filepath.Join(s.RootDir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello cassandra\n" {
		t.Errorf("expected 'hello cassandra\\n', got %q", string(content))
	}

	// 5. Verify git status
	err = s.runGit(ctx, "log", "--oneline")
	if err != nil {
		t.Errorf("git log failed: %v", err)
	}
}
