package tracing

import (
	"net/http"
	"net/http/httptest"
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

func TestMiddleware_CreatesSpan(t *testing.T) {
	tracer := NewTracer("test-tracer")

	var traceID, spanID string
	handler := Middleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, ok := ctxkeys.TraceIDFrom(r.Context())
		assert.True(t, ok)
		traceID = tid

		sid, ok := ctxkeys.SpanIDFrom(r.Context())
		assert.True(t, ok)
		spanID = sid

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, traceID)
	assert.NotEmpty(t, spanID)
}

func TestMiddleware_CapturesStatus(t *testing.T) {
	tracer := NewTracer("test-tracer")

	handler := Middleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMiddleware_UniqueSpanIDs(t *testing.T) {
	tracer := NewTracer("test-tracer")
	spanIDs := make(map[string]bool)

	handler := Middleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid, _ := ctxkeys.SpanIDFrom(r.Context())
		assert.False(t, spanIDs[sid], "duplicate span ID: %s", sid)
		spanIDs[sid] = true
	}))

	for range 50 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
