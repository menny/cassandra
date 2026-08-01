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

	var foundGoBin string
	var goroot string

	// Check candidate path derived directly from rpath first (O(1))
	candidate := filepath.Join(runfilesDir, rpath)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		foundGoBin = candidate
	}

	// Only scan top-level runfiles entries if foundGoBin or GOROOT is not yet resolved
	if foundGoBin == "" || goroot == "" {
		entries, err := os.ReadDir(runfilesDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() && strings.Contains(entry.Name(), "go_sdk") {
					sdkPath := filepath.Join(runfilesDir, entry.Name())
					goExecutable := filepath.Join(sdkPath, "bin", "go")
					if info, err := os.Stat(goExecutable); err == nil && !info.IsDir() {
						foundGoBin = goExecutable
						goroot = sdkPath
						break
					}
					if goroot == "" {
						goroot = sdkPath
					}
				}
			}
		}
	}

	if foundGoBin == "" {
		panic(fmt.Sprintf("GO_BIN_RLOCATION is set to %q, but failed to locate Go SDK binary under %q", rpath, runfilesDir))
	}

	if err := os.Setenv("GO_BIN", foundGoBin); err != nil {
		panic(fmt.Sprintf("failed to set GO_BIN environment variable: %v", err))
	}

	if goroot != "" {
		if err := os.Setenv("GOROOT", goroot); err != nil {
			panic(fmt.Sprintf("failed to set GOROOT environment variable: %v", err))
		}
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
