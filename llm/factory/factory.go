// Package factory constructs llm.Model instances for the supported providers.
// main.go uses this package; all other packages depend only on llm.Model.
package factory

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/llm/anthropic"
	"github.com/menny/cassandra/llm/google"
	"github.com/menny/cassandra/llm/openai"
)

// Provider identifies a supported LLM provider.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderGoogle    Provider = "google"
	ProviderOpenAI    Provider = "openai"
)

// providerFactory constructs a provider-specific llm.Model. Implementations
// may use ctx (e.g. to dial eagerly) or ignore it. baseURL overrides the
// provider's default API endpoint; pass an empty string for the default.
type providerFactory func(ctx context.Context, apiKey, modelName, baseURL string, options map[string]any) (llm.Model, error)

// providers is the registry of supported Providers. Adding a new provider
// is a single entry here; factory.New and the "unsupported provider" error
// message derive from this map automatically.
var providers = map[Provider]providerFactory{
	ProviderAnthropic: func(_ context.Context, apiKey, modelName, baseURL string, options map[string]any) (llm.Model, error) {
		if baseURL != "" {
			fmt.Fprintf(os.Stderr, "Warning: baseURL %q is ignored by the anthropic provider\n", baseURL)
		}
		// Anthropic client is constructed synchronously; ctx is unused at
		// construction time (the SDK dials lazily per request).
		return anthropic.New(apiKey, modelName, options), nil
	},
	ProviderGoogle: func(ctx context.Context, apiKey, modelName, baseURL string, options map[string]any) (llm.Model, error) {
		if baseURL != "" {
			fmt.Fprintf(os.Stderr, "Warning: baseURL %q is ignored by the google provider\n", baseURL)
		}
		return google.New(ctx, apiKey, modelName, options)
	},
	ProviderOpenAI: func(_ context.Context, apiKey, modelName, baseURL string, options map[string]any) (llm.Model, error) {
		// OpenAI client is constructed synchronously; ctx is unused at
		// construction time (the SDK dials lazily per request).
		return openai.New(apiKey, modelName, baseURL, options), nil
	},
}

// New constructs a Model for the given provider, model name, and API key.
// baseURL overrides the provider's default API endpoint; pass an empty string
// to use the official endpoint. This is only honoured by the OpenAI provider,
// allowing callers to target OpenAI-compatible services (e.g. Ollama).
// The returned model automatically retries transient errors with exponential
// back-off using llm.DefaultRetryAttempts and llm.DefaultRetryBaseDelay.
func New(ctx context.Context, provider, modelName, apiKey, baseURL string, options map[string]any) (llm.Model, error) {
	f, ok := providers[Provider(provider)]
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q: supported providers are %s",
			provider, supportedProviders())
	}
	inner, err := f(ctx, apiKey, modelName, baseURL, options)
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
