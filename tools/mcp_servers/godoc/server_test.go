package godoc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
