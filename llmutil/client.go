package llmutil

import (
	"context"
	"fmt"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/googleai"
)

// NewClient returns a unified LLM model interface
func NewClient(ctx context.Context, provider, modelName, apiKey string) (llms.Model, error) {
	switch provider {
	case "google":
		return googleai.New(ctx, googleai.WithAPIKey(apiKey), googleai.WithDefaultModel(modelName))
	case "anthropic":
		return anthropic.New(anthropic.WithToken(apiKey), anthropic.WithModel(modelName))
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}
