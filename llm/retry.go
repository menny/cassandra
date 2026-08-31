package llm

import (
	"context"
	"time"
)

const (
	// DefaultRetryAttempts is the number of total attempts (1 initial + 9 retries).
	DefaultRetryAttempts = 10
	// DefaultRetryBaseDelay is the starting back-off delay between attempts.
	DefaultRetryBaseDelay = time.Second
	// DefaultRetryMaxDelay is the maximum back-off delay between attempts.
	DefaultRetryMaxDelay = 15 * time.Second
)

// RetryingModel wraps any Model and transparently retries on any error
// (network failures, rate limits, server errors, etc.) using exponential
// back-off capped at a maximum delay. It implements the Model interface.
type RetryingModel struct {
	inner       Model
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// NewRetryingModel returns a Model that retries failed calls up to maxAttempts
// times total (i.e. 1 initial attempt + maxAttempts-1 retries), doubling the
// delay after each failure starting from baseDelay up to DefaultRetryMaxDelay.
//
// The wrapper respects context cancellation: if ctx is cancelled between
// attempts, the last error is returned immediately without further retries.
func NewRetryingModel(inner Model, maxAttempts int, baseDelay time.Duration) *RetryingModel {
	return NewRetryingModelWithMaxDelay(inner, maxAttempts, baseDelay, DefaultRetryMaxDelay)
}

// NewRetryingModelWithMaxDelay returns a Model that retries failed calls up to
// maxAttempts times total with exponential back-off capped at maxDelay.
func NewRetryingModelWithMaxDelay(inner Model, maxAttempts int, baseDelay, maxDelay time.Duration) *RetryingModel {
	if maxAttempts <= 0 {
		maxAttempts = DefaultRetryAttempts
	}
	if baseDelay <= 0 {
		baseDelay = DefaultRetryBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = DefaultRetryMaxDelay
	}
	return &RetryingModel{inner: inner, maxAttempts: maxAttempts, baseDelay: baseDelay, maxDelay: maxDelay}
}

// retry invokes fn up to maxAttempts times, doubling the delay between
// attempts starting from baseDelay up to maxDelay. It returns early if ctx is cancelled.
func retry[T any](ctx context.Context, maxAttempts int, baseDelay, maxDelay time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	var lastErr error
	delay := baseDelay
	if delay > maxDelay {
		delay = maxDelay
	}
	for attempt := range maxAttempts {
		if attempt > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
			}
			if delay < maxDelay {
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
			}
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
	return retry(ctx, r.maxAttempts, r.baseDelay, r.maxDelay, func(ctx context.Context) (*Response, error) {
		return r.inner.GenerateContent(ctx, messages, tools, maxTokens)
	})
}

// GenerateStructuredContent calls the underlying model, retrying on any error.
func (r *RetryingModel) GenerateStructuredContent(ctx context.Context, messages []Message, schema map[string]any, config StructuredConfig) (*Response, error) {
	return retry(ctx, r.maxAttempts, r.baseDelay, r.maxDelay, func(ctx context.Context) (*Response, error) {
		return r.inner.GenerateStructuredContent(ctx, messages, schema, config)
	})
}
