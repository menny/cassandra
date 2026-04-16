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

// RetryingModel wraps any Model and transparently retries transient errors
// using exponential back-off. It implements the Model interface.
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

// GenerateContent calls the underlying model, retrying on any error.
func (r *RetryingModel) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, maxTokens int) (*Response, error) {
	var lastErr error
	delay := r.baseDelay
	for attempt := range r.maxAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				delay *= 2
			}
		}
		resp, err := r.inner.GenerateContent(ctx, messages, tools, maxTokens)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// GenerateStructuredContent calls the underlying model, retrying on any error.
func (r *RetryingModel) GenerateStructuredContent(ctx context.Context, messages []Message, schema map[string]any, config StructuredConfig) (*Response, error) {
	var lastErr error
	delay := r.baseDelay
	for attempt := range r.maxAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				delay *= 2
			}
		}
		resp, err := r.inner.GenerateStructuredContent(ctx, messages, schema, config)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}
