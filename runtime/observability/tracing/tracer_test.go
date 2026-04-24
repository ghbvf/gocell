package tracing

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
)

func TestNewTracer_Start(t *testing.T) {
	tracer := NewTracer("test-service")
	ctx, span := tracer.Start(t.Context(), "test-operation")
	defer span.End()

	simple, ok := span.(*simpleSpan)
	assert.True(t, ok, "simpleTracer must return *simpleSpan")
	assert.NotEmpty(t, simple.TraceID())
	assert.NotEmpty(t, simple.SpanID())
	assert.Len(t, simple.TraceID(), 32) // 16 bytes hex-encoded
	assert.Len(t, simple.SpanID(), 16)  // 8 bytes hex-encoded

	// Context should carry trace/span IDs.
	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, simple.TraceID(), traceID)

	spanID, ok := ctxkeys.SpanIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, simple.SpanID(), spanID)
}

func TestNewTracer_InheritsParentTraceID(t *testing.T) {
	tracer := NewTracer("test-service")
	ctx := ctxkeys.WithTraceID(t.Context(), "parent-trace-id")

	_, span := tracer.Start(ctx, "child-operation")
	defer span.End()

	simple := span.(*simpleSpan)
	assert.Equal(t, "parent-trace-id", simple.TraceID())
	assert.NotEmpty(t, simple.SpanID())
}

func TestSpan_SetAttributes_DoesNotPanic(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	// kernel/wrapper.Span API: variadic Attr, not per-key SetAttribute.
	span.SetAttributes(
		Attr{Key: "http.method", Value: "GET"},
		Attr{Key: "http.status_code", Value: 200},
	)
}

func TestSimpleSpan_End(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	assert.NotPanics(t, func() { span.End() })
}

func TestSpanRecordError_RecordsOnSimpleSpan(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	testErr := errors.New("connection refused")
	SpanRecordError(span, testErr)
	assert.ErrorIs(t, span.(*simpleSpan).err, testErr)
}

func TestSpanSetStatus_MapsLegacyBool(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	SpanSetStatus(span, true, "db timeout")
	assert.Equal(t, wrapper.StatusError, span.(*simpleSpan).status)
	assert.Equal(t, "db timeout", span.(*simpleSpan).stDesc)

	SpanSetStatus(span, false, "")
	assert.Equal(t, wrapper.StatusOK, span.(*simpleSpan).status)
}

func TestSpanSetName_SimpleSpanSupportsRename(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "initial")
	defer span.End()

	// simpleSpan implements wrapper.SpanRenamer, so SpanSetName MUST take effect.
	SpanSetName(span, "POST /api/v1/auth/login")
	assert.Equal(t, "POST /api/v1/auth/login", span.(*simpleSpan).name)
}

// staticSpan is a minimal Span that does NOT implement wrapper.SpanRenamer —
// SetSpanName should silently skip it without panicking.
type staticSpan struct{ name string }

func (s *staticSpan) SetAttributes(_ ...Attr)                  {}
func (s *staticSpan) RecordError(_ error)                      {}
func (s *staticSpan) SetStatus(_ wrapper.StatusCode, _ string) {}
func (s *staticSpan) End()                                     {}

func TestSpanSetName_GracefullyIgnoresNonRenamerSpans(t *testing.T) {
	span := &staticSpan{name: "original"}
	assert.NotPanics(t, func() { SpanSetName(span, "other") })
	assert.Equal(t, "original", span.name, "static span must not be renamed")
}

func TestSimpleSpan_ConcurrentMutationSafe(t *testing.T) {
	tracer := NewTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			span.SetAttributes(Attr{Key: "attempt", Value: int64(i)})
			span.RecordError(fmt.Errorf("err-%d", i))
			span.SetStatus(wrapper.StatusError, fmt.Sprintf("status-%d", i))
			SpanSetName(span, fmt.Sprintf("op-%d", i))
		}(i)
	}
	wg.Wait()
}
