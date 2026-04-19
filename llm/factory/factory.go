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
// The returned model automatically retries transient errors with exponential
// back-off using llm.DefaultRetryAttempts and llm.DefaultRetryBaseDelay.
func New(ctx context.Context, provider, modelName, apiKey string) (llm.Model, error) {
	var inner llm.Model
	switch Provider(provider) {
	case ProviderAnthropic:
		// Anthropic client is constructed synchronously; ctx is unused at
		// construction time (the SDK dials lazily per request).
		inner = anthropic.New(apiKey, modelName)
	case ProviderGoogle:
		var err error
		inner, err = google.New(ctx, apiKey, modelName)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported provider %q: supported providers are %q and %q",
			provider, ProviderAnthropic, ProviderGoogle)
	}
	return llm.NewRetryingModel(inner, llm.DefaultRetryAttempts, llm.DefaultRetryBaseDelay), nil
}
