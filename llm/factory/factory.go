// Package factory constructs llm.Model instances for the supported providers.
// main.go uses this package; all other packages depend only on llm.Model.
package factory

import (
	"context"
	"fmt"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/anthropic"
	"github.com/menny/cassandra/llm/google"
)

// New constructs a Model for the given provider, model name, and API key.
func New(ctx context.Context, provider, modelName, apiKey string) (llm.Model, error) {
	switch llm.Provider(provider) {
	case llm.ProviderAnthropic:
		return anthropic.New(apiKey, modelName), nil
	case llm.ProviderGoogle:
		return google.New(ctx, apiKey, modelName)
	default:
		return nil, fmt.Errorf("unsupported provider %q: supported providers are %q and %q",
			provider, llm.ProviderAnthropic, llm.ProviderGoogle)
	}
}
