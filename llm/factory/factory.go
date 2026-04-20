// Package factory constructs llm.Model instances for the supported providers.
// main.go uses this package; all other packages depend only on llm.Model.
package factory

import (
	"context"
	"fmt"
	"sort"
	"strings"

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

// providerFactory constructs a provider-specific llm.Model. Implementations
// may use ctx (e.g. to dial eagerly) or ignore it.
type providerFactory func(ctx context.Context, apiKey, modelName string) (llm.Model, error)

// providers is the registry of supported Providers. Adding a new provider
// is a single entry here; factory.New and the "unsupported provider" error
// message derive from this map automatically.
var providers = map[Provider]providerFactory{
	ProviderAnthropic: func(_ context.Context, apiKey, modelName string) (llm.Model, error) {
		// Anthropic client is constructed synchronously; ctx is unused at
		// construction time (the SDK dials lazily per request).
		return anthropic.New(apiKey, modelName), nil
	},
	ProviderGoogle: func(ctx context.Context, apiKey, modelName string) (llm.Model, error) {
		return google.New(ctx, apiKey, modelName)
	},
}

// New constructs a Model for the given provider, model name, and API key.
// The returned model automatically retries transient errors with exponential
// back-off using llm.DefaultRetryAttempts and llm.DefaultRetryBaseDelay.
func New(ctx context.Context, provider, modelName, apiKey string) (llm.Model, error) {
	f, ok := providers[Provider(provider)]
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q: supported providers are %s",
			provider, supportedProviders())
	}
	inner, err := f(ctx, apiKey, modelName)
	if err != nil {
		return nil, err
	}
	return llm.NewRetryingModel(inner, llm.DefaultRetryAttempts, llm.DefaultRetryBaseDelay), nil
}

// supportedProviders returns a sorted, comma-separated list of known
// provider identifiers for use in error messages.
func supportedProviders() string {
	names := make([]string, 0, len(providers))
	for p := range providers {
		names = append(names, fmt.Sprintf("%q", string(p)))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
