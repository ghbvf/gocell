package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/stretchr/testify/assert"
)

func TestTracing_CreatesSpan(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

	var traceID, spanID string
	handler := Tracing(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestTracing_CapturesStatus(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

	handler := Tracing(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTracing_UniqueSpanIDs(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")
	spanIDs := make(map[string]bool)

	handler := Tracing(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
