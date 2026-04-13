package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestTracing_PropagatesUpstreamTraceID(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

	tests := []struct {
		name         string
		headers      map[string]string
		wantTraceID  string
		parentSpanID string
	}{
		{
			name: "w3c traceparent",
			headers: map[string]string{
				"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			parentSpanID: "00f067aa0ba902b7",
		},
		{
			name: "b3 single header",
			headers: map[string]string{
				"b3": "4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			parentSpanID: "00f067aa0ba902b7",
		},
		{
			name: "b3 multi header",
			headers: map[string]string{
				"X-B3-TraceId": "4bf92f3577b34da6a3ce929d0e0e4736",
				"X-B3-SpanId":  "00f067aa0ba902b7",
				"X-B3-Sampled": "1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			parentSpanID: "00f067aa0ba902b7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTraceID string
			var gotSpanID string

			handler := Tracing(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var ok bool
				gotTraceID, ok = ctxkeys.TraceIDFrom(r.Context())
				require.True(t, ok)

				gotSpanID, ok = ctxkeys.SpanIDFrom(r.Context())
				require.True(t, ok)
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/propagated", nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantTraceID, gotTraceID,
				"trace_id should reuse the upstream propagated trace")
			assert.NotEqual(t, tt.parentSpanID, gotSpanID,
				"server span must get a fresh span_id even when it inherits a trace")
		})
	}
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

// --- Chi-integrated tests for span renaming ---

// spySpan records attributes and name changes for testing.
type spySpan struct {
	mu    sync.Mutex
	name  string
	attrs map[string]any
}

func (s *spySpan) End()                  {}
func (s *spySpan) TraceID() string       { return "spy-trace" }
func (s *spySpan) SpanID() string        { return "spy-span" }
func (s *spySpan) SetName(name string)   { s.mu.Lock(); s.name = name; s.mu.Unlock() }
func (s *spySpan) SetAttribute(key string, val any) {
	s.mu.Lock()
	s.attrs[key] = val
	s.mu.Unlock()
}

// SpanRecorder methods — capture SetStatus/RecordError calls.
func (s *spySpan) SetStatus(isError bool, description string) {
	s.mu.Lock()
	s.attrs["_status_error"] = isError
	s.attrs["_status_desc"] = description
	s.mu.Unlock()
}

func (s *spySpan) RecordError(err error) {
	s.mu.Lock()
	s.attrs["_recorded_error"] = err.Error()
	s.mu.Unlock()
}

func (s *spySpan) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

func (s *spySpan) Attr(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs[key]
}

// spyTracer returns spySpans that record name changes and attributes.
type spyTracer struct {
	mu    sync.Mutex
	spans []*spySpan
}

func (st *spyTracer) Start(ctx context.Context, name string) (context.Context, tracing.Span) {
	span := &spySpan{name: name, attrs: make(map[string]any)}
	st.mu.Lock()
	st.spans = append(st.spans, span)
	st.mu.Unlock()
	ctx = ctxkeys.WithTraceID(ctx, "spy-trace")
	ctx = ctxkeys.WithSpanID(ctx, "spy-span")
	return ctx, span
}

func (st *spyTracer) Spans() []*spySpan {
	st.mu.Lock()
	defer st.mu.Unlock()
	result := make([]*spySpan, len(st.spans))
	copy(result, st.spans)
	return result
}

func TestTracing_SpanRenamedToRoutePattern(t *testing.T) {
	spy := &spyTracer{}

	r := chi.NewRouter()
	r.Use(Tracing(spy))
	r.Get("/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Hit with different IDs — all spans should be renamed to the route pattern.
	for _, id := range []string{"1", "42", "abc"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+id, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	spans := spy.Spans()
	require.Len(t, spans, 3)
	for _, s := range spans {
		assert.Equal(t, "GET /api/v1/users/{id}", s.Name(),
			"span name must use route pattern, not actual path")
		assert.Equal(t, "/api/v1/users/{id}", s.Attr("http.route"),
			"http.route attribute must be the route pattern")
	}
}

func TestTracing_UnmatchedRouteSpanName(t *testing.T) {
	spy := &spyTracer{}

	r := chi.NewRouter()
	r.Use(Tracing(spy))
	r.Get("/exists", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/random-404-path", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "GET unmatched", spans[0].Name(),
		"unmatched route span must use sentinel name")
	assert.Equal(t, "unmatched", spans[0].Attr("http.route"))
}

func TestTracing_HttpRouteAttribute(t *testing.T) {
	spy := &spyTracer{}

	r := chi.NewRouter()
	r.Use(Tracing(spy))
	r.Get("/api/v1/orders/{orderID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/999", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "/api/v1/orders/{orderID}", spans[0].Attr("http.route"))
	assert.Equal(t, 201, spans[0].Attr("http.status_code"))
}

// --- Span status tests (otelhttp alignment) ---

func TestTracing_5xxSetsErrorSpanStatus(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodGet, "/fail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, true, spans[0].Attr("_status_error"),
		"5xx must set span status to error")
	assert.Equal(t, "Internal Server Error", spans[0].Attr("_status_desc"))
}

func TestTracing_4xxDoesNotSetErrorSpanStatus(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Nil(t, spans[0].Attr("_status_error"),
		"4xx must not set span status to error (otelhttp convention)")
}

func TestTracing_2xxDoesNotSetErrorSpanStatus(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Nil(t, spans[0].Attr("_status_error"),
		"2xx must not set span status to error")
}
