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

// Provider identifies a supported LLM provider.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderGoogle    Provider = "google"
)

// New constructs a Model for the given provider, model name, and API key.
func New(ctx context.Context, provider, modelName, apiKey string) (llm.Model, error) {
	switch Provider(provider) {
	case ProviderAnthropic:
		// Anthropic client is constructed synchronously; ctx is unused at
		// construction time (the SDK dials lazily per request).
		return anthropic.New(apiKey, modelName), nil
	case ProviderGoogle:
		return google.New(ctx, apiKey, modelName)
	default:
		return nil, fmt.Errorf("unsupported provider %q: supported providers are %q and %q",
			provider, ProviderAnthropic, ProviderGoogle)
	}
}
