package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubModel is a test double that returns scripted responses in order.
type stubModel struct {
	responses []*Response
	errs      []error
	callCount int
}

func (s *stubModel) GenerateContent(_ context.Context, _ []Message, _ []ToolDef, _ int) (*Response, error) {
	i := s.callCount
	s.callCount++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i < len(s.responses) {
		return s.responses[i], nil
	}
	return nil, errors.New("stubModel: no response configured")
}

func (s *stubModel) GenerateStructuredContent(ctx context.Context, messages []Message, _ map[string]any, _ StructuredConfig) (*Response, error) {
	return s.GenerateContent(ctx, messages, nil, 0)
}

// immediateRetryModel makes retries instant by using a microsecond base delay.
func newInstantRetryModel(inner Model, maxAttempts int) *RetryingModel {
	return NewRetryingModelWithMaxDelay(inner, maxAttempts, time.Microsecond, time.Microsecond)
}

func TestRetryingModel_SuccessOnFirstAttempt(t *testing.T) {
	want := &Response{Text: "hello"}
	stub := &stubModel{responses: []*Response{want}}
	m := newInstantRetryModel(stub, 3)

	got, err := m.GenerateContent(context.Background(), nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1, stub.callCount)
}

func TestRetryingModel_SuccessAfterTransientErrors(t *testing.T) {
	want := &Response{Text: "recovered"}
	stub := &stubModel{
		errs:      []error{errors.New("err1"), errors.New("err2"), nil},
		responses: []*Response{nil, nil, want},
	}
	m := newInstantRetryModel(stub, 3)

	got, err := m.GenerateContent(context.Background(), nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 3, stub.callCount)
}

func TestRetryingModel_ExhaustsAllAttempts(t *testing.T) {
	sentinel := errors.New("permanent failure")
	stub := &stubModel{
		errs: []error{sentinel, sentinel, sentinel},
	}
	m := newInstantRetryModel(stub, 3)

	_, err := m.GenerateContent(context.Background(), nil, nil, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 3, stub.callCount)
}

func TestRetryingModel_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	stub := &stubModel{
		errs: []error{errors.New("transient")},
	}
	// Use a real delay so that the cancelled context is checked before sleeping.
	m := NewRetryingModel(stub, 3, 10*time.Second)

	_, err := m.GenerateContent(ctx, nil, nil, 0)
	require.Error(t, err)
	// After the first failure the wrapper should detect ctx.Err() and stop.
	assert.Equal(t, 1, stub.callCount)
}

func TestRetryingModel_GenerateStructuredContent_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	stub := &stubModel{
		errs: []error{errors.New("transient")},
	}
	// Real delay so the cancelled context is detected before sleeping.
	m := NewRetryingModel(stub, 3, 10*time.Second)

	_, err := m.GenerateStructuredContent(ctx, nil, nil, StructuredConfig{})
	require.Error(t, err)
	assert.Equal(t, 1, stub.callCount)
}

func TestRetryingModel_GenerateStructuredContent_ExhaustsAllAttempts(t *testing.T) {
	sentinel := errors.New("permanent failure")
	stub := &stubModel{
		errs: []error{sentinel, sentinel, sentinel},
	}
	m := newInstantRetryModel(stub, 3)

	_, err := m.GenerateStructuredContent(context.Background(), nil, nil, StructuredConfig{})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 3, stub.callCount)
}

func TestRetryingModel_GenerateStructuredContent_Retries(t *testing.T) {
	want := &Response{Text: `{"ok":true}`}
	stub := &stubModel{
		errs:      []error{errors.New("transient"), nil},
		responses: []*Response{nil, want},
	}
	m := newInstantRetryModel(stub, 3)

	got, err := m.GenerateStructuredContent(context.Background(), nil, nil, StructuredConfig{})
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 2, stub.callCount)
}

func TestNewRetryingModel_Defaults(t *testing.T) {
	stub := &stubModel{responses: []*Response{{Text: "ok"}}}
	m := NewRetryingModel(stub, 0, 0)
	assert.Equal(t, DefaultRetryAttempts, m.maxAttempts)
	assert.Equal(t, DefaultRetryBaseDelay, m.baseDelay)
	assert.Equal(t, DefaultRetryMaxDelay, m.maxDelay)

	// Explicit baseDelay larger than DefaultRetryMaxDelay should dynamically raise maxDelay.
	mLarge := NewRetryingModel(stub, 0, 30*time.Second)
	assert.Equal(t, 30*time.Second, mLarge.baseDelay)
	assert.Equal(t, 30*time.Second, mLarge.maxDelay)
}

func TestNewRetryingModelWithMaxDelay_DefaultsAndCustom(t *testing.T) {
	stub := &stubModel{responses: []*Response{{Text: "ok"}}}
	m := NewRetryingModelWithMaxDelay(stub, 0, 0, 0)
	assert.Equal(t, DefaultRetryAttempts, m.maxAttempts)
	assert.Equal(t, DefaultRetryBaseDelay, m.baseDelay)
	assert.Equal(t, DefaultRetryMaxDelay, m.maxDelay)

	mCustom := NewRetryingModelWithMaxDelay(stub, 5, 2*time.Second, 10*time.Second)
	assert.Equal(t, 5, mCustom.maxAttempts)
	assert.Equal(t, 2*time.Second, mCustom.baseDelay)
	assert.Equal(t, 10*time.Second, mCustom.maxDelay)

	// Zero maxDelay with baseDelay > DefaultRetryMaxDelay.
	mLarge := NewRetryingModelWithMaxDelay(stub, 0, 30*time.Second, 0)
	assert.Equal(t, 30*time.Second, mLarge.baseDelay)
	assert.Equal(t, 30*time.Second, mLarge.maxDelay)
}

func TestRetryingModel_BackoffCap(t *testing.T) {
	stub := &stubModel{
		errs: []error{
			errors.New("err1"),
			errors.New("err2"),
			errors.New("err3"),
			nil,
		},
		responses: []*Response{nil, nil, nil, {Text: "ok"}},
	}
	// baseDelay 50ms, maxDelay 60ms.
	// attempt 0: t0
	// attempt 1: wait 50ms -> delay becomes min(100ms, 60ms) = 60ms
	// attempt 2: wait 60ms -> delay remains 60ms
	// attempt 3: wait 60ms -> success
	// Capped total wait: 50ms + 60ms + 60ms = 170ms.
	// (Uncapped would be: 50ms + 100ms + 200ms = 350ms).
	m := NewRetryingModelWithMaxDelay(stub, 5, 50*time.Millisecond, 60*time.Millisecond)

	start := time.Now()
	_, err := m.GenerateContent(context.Background(), nil, nil, 0)
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.Equal(t, 4, stub.callCount)
	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
	assert.Less(t, elapsed, 260*time.Millisecond)
}

func TestRetryingModel_GenerateStructuredContent_BackoffCap(t *testing.T) {
	stub := &stubModel{
		errs: []error{
			errors.New("err1"),
			errors.New("err2"),
			errors.New("err3"),
			nil,
		},
		responses: []*Response{nil, nil, nil, {Text: `{"ok":true}`}},
	}
	// baseDelay 50ms, maxDelay 60ms.
	// Capped total wait: 50ms + 60ms + 60ms = 170ms (vs uncapped 350ms).
	m := NewRetryingModelWithMaxDelay(stub, 5, 50*time.Millisecond, 60*time.Millisecond)

	start := time.Now()
	got, err := m.GenerateStructuredContent(context.Background(), nil, nil, StructuredConfig{})
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.Equal(t, &Response{Text: `{"ok":true}`}, got)
	assert.Equal(t, 4, stub.callCount)
	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
	assert.Less(t, elapsed, 260*time.Millisecond)
}
