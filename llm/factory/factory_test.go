package factory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequiresAPIKey(t *testing.T) {
	assert.True(t, RequiresAPIKey(string(ProviderAnthropic)))
	assert.True(t, RequiresAPIKey(string(ProviderGoogle)))
	assert.True(t, RequiresAPIKey(string(ProviderOpenAI)))
	assert.False(t, RequiresAPIKey(string(ProviderAnthropicBedrock)))
}

func TestNew_UnsupportedProviderListsBedrock(t *testing.T) {
	_, err := New(context.Background(), "nope", "m", "", "", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"anthropic-bedrock"`)
}

func TestNew_BedrockFailsWithoutRegion(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	_, err := New(context.Background(), string(ProviderAnthropicBedrock), "us.anthropic.claude-opus-5-5", "", "", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AWS_REGION")
}
