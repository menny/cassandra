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

// immediateRetryModel makes retries instant by using a zero base delay.
func newInstantRetryModel(inner Model, maxAttempts int) *RetryingModel {
	return NewRetryingModel(inner, maxAttempts, 0)
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
}
