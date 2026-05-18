package tracingtest

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

func TestNewSimpleTracer_Start(t *testing.T) {
	tracer := NewSimpleTracer("test-service")
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

// TestNewSimpleTracer_InheritsParentTraceID covers the intentional INBOUND
// symmetry (B2-A-20): an upstream trace ID that trace_propagation middleware
// placed in ctxkeys is reused instead of starting a fresh root.
func TestNewSimpleTracer_InheritsParentTraceID(t *testing.T) {
	tracer := NewSimpleTracer("test-service")
	ctx := ctxkeys.WithTraceID(t.Context(), "parent-trace-id")

	_, span := tracer.Start(ctx, "child-operation")
	defer span.End()

	simple := span.(*simpleSpan)
	assert.Equal(t, "parent-trace-id", simple.TraceID())
	assert.NotEmpty(t, simple.SpanID())
}

func TestSimpleSpan_SetAttributes_DoesNotPanic(t *testing.T) {
	tracer := NewSimpleTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	span.SetAttributes(
		wrapper.Attr{Key: "http.method", Value: "GET"},
		wrapper.Attr{Key: "http.status_code", Value: 200},
	)
}

func TestSimpleSpan_End(t *testing.T) {
	tracer := NewSimpleTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	assert.NotPanics(t, func() { span.End() })
}

func TestSimpleSpan_RecordError(t *testing.T) {
	tracer := NewSimpleTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	testErr := errors.New("connection refused")
	span.RecordError(testErr)
	assert.ErrorIs(t, span.(*simpleSpan).err, testErr)
}

func TestSimpleSpan_SetStatus(t *testing.T) {
	tracer := NewSimpleTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	span.SetStatus(wrapper.StatusError, "db timeout")
	assert.Equal(t, wrapper.StatusError, span.(*simpleSpan).status)
	assert.Equal(t, "db timeout", span.(*simpleSpan).stDesc)

	span.SetStatus(wrapper.StatusOK, "")
	assert.Equal(t, wrapper.StatusOK, span.(*simpleSpan).status)
}

// TestSimpleSpan_SupportsRename asserts simpleSpan implements
// wrapper.SpanRenamer so wrapper.SetSpanName takes effect — the two-phase
// HTTP span rename ("{method} {path}" → "{method} {routePattern}") relied on
// by runtime/http/middleware tests.
func TestSimpleSpan_SupportsRename(t *testing.T) {
	tracer := NewSimpleTracer("test")
	_, span := tracer.Start(t.Context(), "initial")
	defer span.End()

	wrapper.SetSpanName(span, "POST /api/v1/auth/login")
	assert.Equal(t, "POST /api/v1/auth/login", span.(*simpleSpan).name)
}

func TestSimpleSpan_ConcurrentMutationSafe(t *testing.T) {
	tracer := NewSimpleTracer("test")
	_, span := tracer.Start(t.Context(), "op")
	defer span.End()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			span.SetAttributes(wrapper.Attr{Key: "attempt", Value: int64(i)})
			span.RecordError(fmt.Errorf("err-%d", i))
			span.SetStatus(wrapper.StatusError, fmt.Sprintf("status-%d", i))
			wrapper.SetSpanName(span, fmt.Sprintf("op-%d", i))
		}(i)
	}
	wg.Wait()
}
