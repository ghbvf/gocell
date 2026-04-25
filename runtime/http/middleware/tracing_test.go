package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/go-chi/chi/v5"
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

func TestTracing_UsesUpstreamTraceparent(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

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
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"trace_id should reuse the upstream propagated trace")
	assert.NotEqual(t, "00f067aa0ba902b7", gotSpanID,
		"server span must get a fresh span_id even when it inherits a trace")
}

func TestTracing_InvalidTraceHeadersStartNewRoot(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

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
	req.Header.Set("traceparent", "00-not-a-valid-trace-id-00f067aa0ba902b7-01")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, gotTraceID, 32)
	assert.Len(t, gotSpanID, 16)
	assert.NotEqual(t, "not-a-valid-trace-id", gotTraceID)
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

func (s *spySpan) End()                {}
func (s *spySpan) TraceID() string     { return "spy-trace" }
func (s *spySpan) SpanID() string      { return "spy-span" }
func (s *spySpan) SetName(name string) { s.mu.Lock(); s.name = name; s.mu.Unlock() }

func (s *spySpan) SetAttributes(attrs ...wrapper.Attr) {
	s.mu.Lock()
	for _, a := range attrs {
		s.attrs[a.Key] = a.Value
	}
	s.mu.Unlock()
}

// SetStatus captures the kernel/wrapper-shaped status signal.
func (s *spySpan) SetStatus(code wrapper.StatusCode, description string) {
	s.mu.Lock()
	s.attrs["_status_code"] = code
	s.attrs["_status_error"] = code == wrapper.StatusError
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

func (st *spyTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, tracing.Span) {
	span := &spySpan{name: name, attrs: make(map[string]any)}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
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
	assert.Equal(t, int64(201), spans[0].Attr("http.status_code"))
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

// TestTracing_499_AttrAndUnsetStatus locks the OTel-compliant 499 contract
// from PR-A50+A51: client cancellation MUST NOT set span.Status=Error
// (4xx server spans MUST remain Unset per OTel HTTP semantic conventions),
// but SHOULD record a structured `client.cancel.reason` attribute so
// dashboards distinguish client-direction signals from validation/auth 4xx.
//
// ref: open-telemetry/semantic-conventions http-spans.md — 4xx server span
//
//	status MUST be left Unset; intentional cancellation MUST NOT set
//	error.type.
func TestTracing_499_AttrAndUnsetStatus(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(499)
	}))

	req := httptest.NewRequest(http.MethodGet, "/canceled", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "context_canceled", spans[0].Attr("client.cancel.reason"),
		"499 must attach client.cancel.reason attribute for dashboards")
	assert.Equal(t, int64(499), spans[0].Attr("http.status_code"))
	assert.Nil(t, spans[0].Attr("_status_error"),
		"499 (4xx) MUST NOT set span.Status=Error per OTel conventions")
	assert.Nil(t, spans[0].Attr("_status_desc"),
		"499 must not record a status description (status remains Unset)")
}

// TestTracing_499_ReasonFromCanceled and TestTracing_504_FromDeadline lock
// the PR271-FU1 + PR275 P2-3 contracts:
//
//   - context.Canceled → ErrClientCanceled (HTTP 499) + span attribute
//     client.cancel.reason="canceled" + span.Status Unset (4xx)
//   - context.DeadlineExceeded → ErrServerTimeout (HTTP 504) + span.Status
//     Error (5xx) + NO client.cancel.reason attribute (504 carries the
//     timeout signal in its status code)
//
// Wiring path under test for 499:
//
//	ctxcancel.Wrap(context.Canceled) → *errcode.Error{Code: ErrClientCanceled,
//	                                                  Details["reason"]: "canceled"}
//	    ↓
//	httputil.writeErrcodeError (499 branch) → setCancelReason(ctx, "canceled")
//	    ↓
//	tracing.serveSpanned reads the slot at end-of-request → span attribute
//	"client.cancel.reason" = "canceled".
//
// Wiring path under test for 504:
//
//	ctxcancel.Wrap(context.DeadlineExceeded) → *errcode.Error{Code: ErrServerTimeout, ...}
//	    ↓
//	httputil.writeErrcodeError (5xx branch) → log5xx + sanitized response
//	    ↓
//	tracing.serveSpanned: status >= 500 → span.SetStatus(Error, …); no
//	client.cancel.reason attribute (500-block guard skips the 499-only branch).
func TestTracing_499_ReasonFromCanceled(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ecErr := ctxcancel.Wrap(context.Canceled, "Insert", "id=x")
		require.NotNil(t, ecErr, "ctxcancel.Wrap must produce *errcode.Error for context.Canceled")
		httputil.WriteDomainError(r.Context(), w, ecErr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/canceled-flow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, httputil.StatusClientClosedRequest, rec.Code)
	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "canceled", spans[0].Attr("client.cancel.reason"),
		"context.Canceled must surface as reason=canceled")
	assert.Nil(t, spans[0].Attr("_status_error"),
		"499 must not set span.Status=Error")
}

func TestTracing_504_FromDeadline(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ecErr := ctxcancel.Wrap(context.DeadlineExceeded, "Query", "id=x")
		require.NotNil(t, ecErr, "ctxcancel.Wrap must produce *errcode.Error for context.DeadlineExceeded")
		httputil.WriteDomainError(r.Context(), w, ecErr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/deadline-flow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusGatewayTimeout, rec.Code,
		"context.DeadlineExceeded must surface as HTTP 504 (real server-side timeout), "+
			"not 499 — feeds 5xx alerting + SDK retry-on-504 policies")
	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, int64(504), spans[0].Attr("http.status_code"))
	assert.Equal(t, true, spans[0].Attr("_status_error"),
		"504 (5xx) MUST set span.Status=Error per OTel HTTP semantic conventions")
	assert.Nil(t, spans[0].Attr("client.cancel.reason"),
		"client.cancel.reason is a 499-only attribute; 504 carries the timeout "+
			"signal in its status code and must not piggyback on the 499 attribute")
}

// TestTracing_499_ReasonViaWriteDecodeError pins the slot transit through
// the WriteDecodeError path (PR275 P2-2). WriteDecodeError shares the same
// writeErrcodeError pipeline as WriteDomainError, so in principle the slot
// flows through automatically — but without a test, a future split where
// WriteDecodeError takes a fast path could silently drop reason without
// breaking anything else. This test forces the contract: any 499 emitted
// via writeErrcodeError surface (WriteDomainError, WriteDecodeError, or
// future writers) must populate the slot.
func TestTracing_499_ReasonViaWriteDecodeError(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ecErr := ctxcancel.Wrap(context.Canceled, "Decode", "id=x")
		require.NotNil(t, ecErr)
		httputil.WriteDecodeError(r.Context(), w, ecErr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/decode-canceled", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, httputil.StatusClientClosedRequest, rec.Code)
	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "canceled", spans[0].Attr("client.cancel.reason"),
		"WriteDecodeError must transit reason through the same slot as WriteDomainError")
}

// TestTracing_4xxNoErrorSpanStatus_NoCancelAttr ensures plain 4xx (e.g.
// validation 400) leaves span.Status Unset (already covered by current
// behaviour) AND does NOT spuriously add the client.cancel.reason
// attribute introduced for 499. Guards against accidentally widening the
// attribute to all 4xx responses.
func TestTracing_4xxNoErrorSpanStatus_NoCancelAttr(t *testing.T) {
	spy := &spyTracer{}
	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	req := httptest.NewRequest(http.MethodGet, "/bad", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Nil(t, spans[0].Attr("_status_error"))
	assert.Nil(t, spans[0].Attr("client.cancel.reason"),
		"client.cancel.reason must be 499-only, not generic 4xx")
}

func TestTracing_RecoveryRecordsRedactedPanicError(t *testing.T) {
	spy := &spyTracer{}
	handler := Tracing(spy, WithErrorRedactor(func(error) error {
		return errors.New("redacted panic")
	}))(Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("raw secret token")
	})))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "redacted panic", spans[0].Attr("_recorded_error"))
	assert.Equal(t, true, spans[0].Attr("_status_error"))
	assert.Equal(t, "Internal Server Error", spans[0].Attr("_status_desc"))
}

func TestTracing_RecoveryMarksCommittedPanicSpanError(t *testing.T) {
	spy := &spyTracer{}
	handler := Tracing(spy)(Recovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("panic after commit")
	})))

	req := httptest.NewRequest(http.MethodGet, "/panic-after-commit", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	spans := spy.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "panic after commit", spans[0].Attr("_recorded_error"))
	assert.Equal(t, true, spans[0].Attr("_status_error"))
	assert.Equal(t, "panic", spans[0].Attr("_status_desc"))
	assert.Equal(t, int64(http.StatusOK), spans[0].Attr("http.status_code"))
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

// --- Public endpoint trust boundary tests (#24 TRUST-POLICY-01) ---

func TestTracing_PublicEndpoint_NewRootTrace(t *testing.T) {
	tracer := tracing.NewTracer("public-test")
	upstreamTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"

	var gotTraceID string
	handler := Tracing(tracer, WithPublicEndpointFn(func(r *http.Request) bool {
		return true // all endpoints are public
	}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("traceparent", "00-"+upstreamTraceID+"-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Core semantic: public endpoint must NOT inherit upstream trace.
	assert.NotEqual(t, upstreamTraceID, gotTraceID,
		"public endpoint must create new root trace, not inherit upstream")
	assert.Len(t, gotTraceID, 32, "new root trace ID must be a valid 32-char hex")
}

func TestTracing_PublicEndpoint_LinkedAttributes(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy, WithPublicEndpointFn(func(r *http.Request) bool {
		return true
	}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := spy.Spans()
	require.Len(t, spans, 1)

	// Linked attributes record the remote context for correlation.
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[0].Attr("linked.trace_id"),
		"public endpoint must record incoming trace_id as linked attribute")
	assert.Equal(t, "00f067aa0ba902b7", spans[0].Attr("linked.span_id"),
		"public endpoint must record incoming span_id as linked attribute")
}

func TestTracing_NonPublicEndpoint_InheritsTrace(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

	var gotTraceID string
	handler := Tracing(tracer, WithPublicEndpointFn(func(r *http.Request) bool {
		return false // not public
	}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"non-public endpoint must inherit upstream trace")
}

func TestTracing_PublicEndpoint_NoInboundHeaders(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

	var gotTraceID string
	handler := Tracing(tracer, WithPublicEndpointFn(func(r *http.Request) bool {
		return true
	}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/public-no-headers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEmpty(t, gotTraceID, "new root span should still generate trace ID")
	assert.Len(t, gotTraceID, 32)
}

func TestTracing_NilPublicEndpointFn_AllTrusted(t *testing.T) {
	tracer := tracing.NewTracer("test-tracer")

	var gotTraceID string
	// No WithPublicEndpointFn option → all endpoints trusted (backward compat)
	handler := Tracing(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceID, _ = ctxkeys.TraceIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/default", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"nil publicEndpointFn must default to trusted upstream (backward compat)")
}

// TestTracing_SingleSpanOwnership verifies round-4 F1: the outer
// middleware is the sole HTTP server span owner. kernel/wrapper.HTTPHandler
// no longer creates an inner span — it only writes ctxkeys.ContractID +
// pushes contract attrs into the shared AttrCarrier that this middleware
// late-binds onto its one span. Exactly one span per request, always.
func TestTracing_SingleSpanOwnership(t *testing.T) {
	spy := &spyTracer{}

	handler := Tracing(spy)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate what kernel/wrapper.HTTPHandler does: append contract
		// attrs to the AttrCarrier installed by the middleware. The
		// middleware must late-bind these to its span after next returns.
		if carrier, ok := wrapper.AttrCarrierFrom(r.Context()); ok {
			carrier.Attrs = append(carrier.Attrs, wrapper.Attr{
				Key: "gocell.contract.id", Value: "http.orders.get.v1",
			})
		}
		w.WriteHeader(http.StatusOK)
	}))

	ctx := context.Background()
	ctx = kernelctxkeys.WithContractID(ctx, "http.orders.get.v1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Exactly one span regardless of whether ContractID was already in ctx
	// before this middleware (the skip logic that caused double-counting
	// was removed in round-4).
	spans := spy.Spans()
	require.Len(t, spans, 1, "Tracing must be the single HTTP span owner")

	// The carrier-contributed attr must land on the span post-routing.
	spans[0].mu.Lock()
	gotContractID := spans[0].attrs["gocell.contract.id"]
	spans[0].mu.Unlock()
	assert.Equal(t, "http.orders.get.v1", gotContractID,
		"contract attrs must late-bind onto the single span")
}
