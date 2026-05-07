package eval

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestExtractTarGz_TarSlip(t *testing.T) {
	// 1. Setup a clean destination and a parent directory we want to protect
	parentDir := t.TempDir()
	dstDir := filepath.Join(parentDir, "sandbox")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 2. Create a malicious tar.gz in memory
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// This path attempts to escape dstDir and write into parentDir
	maliciousPath := "../../evil.txt"
	content := "unauthorized access"

	hdr := &tar.Header{
		Name: maliciousPath,
		Mode: 0644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}

	tw.Close()
	gw.Close()

	// 3. Write it to disk for the extractor to read
	tarPath := filepath.Join(parentDir, "malicious.tar.gz")
	if err := os.WriteFile(tarPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Attempt extraction
	err := extractTarGz(tarPath, dstDir)

	// 5. Assert failure
	if err == nil {
		t.Error("Expected error for malicious tarball, but got nil")
	}
	if !strings.Contains(err.Error(), "tar slip detected") {
		t.Errorf("Expected 'tar slip detected' error, got: %v", err)
	}

	// 6. Final safety check: Verify the file was NOT created outside the sandbox
	evilFilePath := filepath.Join(parentDir, "evil.txt")
	if _, err := os.Stat(evilFilePath); err == nil {
		t.Errorf("CRITICAL SECURITY FAILURE: Malicious file was extracted to %s", evilFilePath)
	}
}

func TestExtractTarGz_Truncation(t *testing.T) {
	tmpDir := t.TempDir()
	dstDir := filepath.Join(tmpDir, "dst")
	os.MkdirAll(dstDir, 0755)

	// 1. Create an existing file with long content
	targetFile := filepath.Join(dstDir, "data.txt")
	originalContent := "This is a very long piece of content that should be overwritten completely."
	if err := os.WriteFile(targetFile, []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Create a tarball with the same filename but shorter content
	shortContent := "Short."
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "data.txt",
		Mode: 0644,
		Size: int64(len(shortContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(shortContent)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	tarPath := filepath.Join(tmpDir, "update.tar.gz")
	os.WriteFile(tarPath, buf.Bytes(), 0644)

	// 3. Extract
	if err := extractTarGz(tarPath, dstDir); err != nil {
		t.Fatalf("Extraction failed: %v", err)
	}

	// 4. Verify content is exactly the short content (no trailing bytes from the long content)
	got, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != shortContent {
		t.Errorf("Expected content %q, got %q. Truncation failed.", shortContent, string(got))
	}
}
