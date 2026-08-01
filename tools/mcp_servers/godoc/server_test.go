package godoc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func init() {
	rpath := os.Getenv("GO_BIN_RLOCATION")
	if rpath == "" {
		return
	}

	runfilesDir := os.Getenv("RUNFILES_DIR")
	if runfilesDir == "" {
		runfilesDir = os.Getenv("TEST_SRCDIR")
	}

	if runfilesDir == "" {
		panic(fmt.Sprintf("GO_BIN_RLOCATION is set to %q, but neither RUNFILES_DIR nor TEST_SRCDIR is set", rpath))
	}

	candidate := filepath.Join(runfilesDir, rpath)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		panic(fmt.Sprintf("GO_BIN_RLOCATION is set to %q, but candidate %q cannot be found: %v", rpath, candidate, err))
	}

	var goroot string
	entries, err := os.ReadDir(runfilesDir)
	if err != nil {
		panic(fmt.Sprintf("failed to read runfiles directory %q: %v", runfilesDir, err))
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(entry.Name(), "go_sdk") {
			goroot = filepath.Join(runfilesDir, entry.Name())
			break
		}
	}

	if goroot == "" {
		panic(fmt.Sprintf("failed to locate Go SDK GOROOT directory under %q", runfilesDir))
	}

	goBin := filepath.Join(goroot, "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = candidate
	}

	if err := os.Setenv("GO_BIN", goBin); err != nil {
		panic(fmt.Sprintf("failed to set GO_BIN environment variable: %v", err))
	}

	if err := os.Setenv("GOROOT", goroot); err != nil {
		panic(fmt.Sprintf("failed to set GOROOT environment variable: %v", err))
	}
}

func TestRunGoDoc(t *testing.T) {
	ctx := context.Background()

	t.Run("valid package", func(t *testing.T) {
		output, err := runGoDoc(ctx, "fmt")
		assert.NoError(t, err)
		assert.Contains(t, output, "package fmt")
		assert.Contains(t, output, "import \"fmt\"")
	})

	t.Run("valid symbol", func(t *testing.T) {
		output, err := runGoDoc(ctx, "fmt.Printf")
		assert.NoError(t, err)
		assert.Contains(t, output, "func Printf")
	})

	t.Run("invalid package", func(t *testing.T) {
		_, err := runGoDoc(ctx, "nonexistent_package_12345")
		assert.Error(t, err)
	})
}
