package godoc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/stretchr/testify/assert"
)

func init() {
	if rpath := os.Getenv("GO_BIN_RLOCATION"); rpath != "" {
		if absPath, err := runfiles.Rlocation(rpath); err == nil && absPath != "" {
			idx := strings.Index(absPath, ".runfiles/")
			if idx != -1 {
				runfilesRoot := absPath[:idx+len(".runfiles")]
				var foundGoBin string
				_ = filepath.Walk(runfilesRoot, func(path string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && info.Name() == "go" && strings.Contains(path, "go_sdk") {
						foundGoBin = path
						return filepath.SkipAll
					}
					return nil
				})
				if foundGoBin != "" {
					absPath = foundGoBin
				}
			}
			_ = os.Setenv("GO_BIN", absPath)
			goroot := filepath.Dir(filepath.Dir(absPath))
			_ = os.Setenv("GOROOT", goroot)
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
