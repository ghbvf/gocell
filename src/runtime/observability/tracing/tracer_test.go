package tracing

import (
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

