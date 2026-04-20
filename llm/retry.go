package llm

import (
	"context"
	"time"
)

const (
	// DefaultRetryAttempts is the number of total attempts (1 initial + 2 retries).
	DefaultRetryAttempts = 3
	// DefaultRetryBaseDelay is the starting back-off delay between attempts.
	DefaultRetryBaseDelay = time.Second
)

// RetryingModel wraps any Model and transparently retries on any error
// (network failures, rate limits, server errors, etc.) using exponential
// back-off. It implements the Model interface.
type RetryingModel struct {
	inner       Model
	maxAttempts int
	baseDelay   time.Duration
}

// NewRetryingModel returns a Model that retries failed calls up to maxAttempts
// times total (i.e. 1 initial attempt + maxAttempts-1 retries), doubling the
// delay after each failure starting from baseDelay.
//
// The wrapper respects context cancellation: if ctx is cancelled between
// attempts, the last error is returned immediately without further retries.
func NewRetryingModel(inner Model, maxAttempts int, baseDelay time.Duration) *RetryingModel {
	if maxAttempts <= 0 {
		maxAttempts = DefaultRetryAttempts
	}
	if baseDelay <= 0 {
		baseDelay = DefaultRetryBaseDelay
	}
	return &RetryingModel{inner: inner, maxAttempts: maxAttempts, baseDelay: baseDelay}
}

// retry invokes fn up to maxAttempts times, doubling the delay between
// attempts starting from baseDelay. It returns early if ctx is cancelled.
func retry[T any](ctx context.Context, maxAttempts int, baseDelay time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	var lastErr error
	delay := baseDelay
	for attempt := range maxAttempts {
		if attempt > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
			}
			delay *= 2
		}
		resp, err := fn(ctx)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
	}
	return zero, lastErr
}

// GenerateContent calls the underlying model, retrying on any error.
func (r *RetryingModel) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, maxTokens int) (*Response, error) {
	return retry(ctx, r.maxAttempts, r.baseDelay, func(ctx context.Context) (*Response, error) {
		return r.inner.GenerateContent(ctx, messages, tools, maxTokens)
	})
}

// GenerateStructuredContent calls the underlying model, retrying on any error.
func (r *RetryingModel) GenerateStructuredContent(ctx context.Context, messages []Message, schema map[string]any, config StructuredConfig) (*Response, error) {
	return retry(ctx, r.maxAttempts, r.baseDelay, func(ctx context.Context) (*Response, error) {
		return r.inner.GenerateStructuredContent(ctx, messages, schema, config)
	})
}
