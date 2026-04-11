package tracing

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
)

func TestNewTracer_Start(t *testing.T) {
	tracer := NewTracer("test-service")
	ctx, span := tracer.Start(t.Context(), "test-operation")
	defer span.End()

	assert.NotEmpty(t, span.TraceID())
	assert.NotEmpty(t, span.SpanID())
	assert.Len(t, span.TraceID(), 32) // 16 bytes hex-encoded
	assert.Len(t, span.SpanID(), 16)  // 8 bytes hex-encoded

	// Context should carry trace/span IDs.
	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, span.TraceID(), traceID)

	spanID, ok := ctxkeys.SpanIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, span.SpanID(), spanID)
}

func TestNewTracer_InheritsParentTraceID(t *testing.T) {
	tracer := NewTracer("test-service")
	ctx := ctxkeys.WithTraceID(t.Context(), "parent-trace-id")

	_, span := tracer.Start(ctx, "child-operation")
	defer span.End()

	assert.Equal(t, "parent-trace-id", span.TraceID())
	assert.NotEmpty(t, span.SpanID())
}

func TestSpan_SetAttribute(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	// Should not panic.
	span.SetAttribute("http.method", "GET")
	span.SetAttribute("http.status_code", 200)
}

func TestSimpleSpan_End(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	assert.NotPanics(t, func() { span.End() })
}

func TestSpanRecordError_SimpleSpan(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	// simpleSpan does not implement SpanRecorder — should be a no-op, not panic.
	assert.NotPanics(t, func() {
		SpanRecordError(span, errors.New("test error"))
	})
}

func TestSpanSetStatus_SimpleSpan(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	assert.NotPanics(t, func() {
		SpanSetStatus(span, true, "failed")
	})
	assert.NotPanics(t, func() {
		SpanSetStatus(span, false, "")
	})
}

// mockRecorderSpan implements both Span and SpanRecorder for testing helpers.
type mockRecorderSpan struct {
	simpleSpan
	recordedErr  error
	statusSet    bool
	statusDesc   string
}

func (m *mockRecorderSpan) RecordError(err error) { m.recordedErr = err }
func (m *mockRecorderSpan) SetStatus(isError bool, desc string) {
	m.statusSet = isError
	m.statusDesc = desc
}

func TestSpanRecordError_WithRecorder(t *testing.T) {
	span := &mockRecorderSpan{}
	testErr := errors.New("connection refused")

	SpanRecordError(span, testErr)
	assert.Equal(t, testErr, span.recordedErr)
}

func TestSpanSetStatus_WithRecorder(t *testing.T) {
	span := &mockRecorderSpan{}

	SpanSetStatus(span, true, "db timeout")
	assert.True(t, span.statusSet)
	assert.Equal(t, "db timeout", span.statusDesc)
}

