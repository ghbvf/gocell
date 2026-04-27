package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket" //nolint:staticcheck // pre-existing dep; coder fork not yet adopted

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/authtest"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCell struct{ *cell.BaseCell }

func newStubCell(id string) *stubCell {
	return &stubCell{BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore})}
}

func findAccessLogEntry(logs []byte, wantPath string) (map[string]any, bool) {
	for _, line := range bytes.Split(logs, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] == "http request" && entry["path"] == wantPath {
			return entry, true
		}
	}
	return nil, false
}

func TestRouterImplementsRouteMux(t *testing.T) {
	r := New()
	var mux cell.RouteMux = r
	assert.NotNil(t, mux)
}

func TestHealthEndpoints(t *testing.T) {
	// PR-A14b: health endpoints live on a dedicated HealthListener router.
	// They are registered directly on the router, not via WithHealthHandler.
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	r, err := NewForListener(cell.HealthListener)
	require.NoError(t, err)
	r.Handle("/healthz", hh.LivezHandler())
	r.Handle("/readyz", hh.ReadyzHandler())

	// Test /healthz
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var envelope map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	data, ok := envelope["data"].(map[string]any)
	require.True(t, ok, "healthz response must carry {\"data\":...} envelope")
	assert.Equal(t, "healthy", data["status"])

	// Test /readyz
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMetricsEndpoint(t *testing.T) {
	// PR-A14b: /metrics lives on the HealthListener router, not the PrimaryListener.
	// Metrics collection is still wired via WithMetricsCollector on the primary router
	// for middleware instrumentation; the /metrics scrape endpoint is a separate handler
	// registered on the HealthListener router by bootstrap.
	mc := metrics.NewInMemoryCollector()
	healthRtr, err := NewForListener(cell.HealthListener)
	require.NoError(t, err)
	healthRtr.Handle("/metrics", mc.Handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	healthRtr.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
}

func TestHandleAndServe(t *testing.T) {
	r := New()
	r.Handle("/test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouteGroup(t *testing.T) {
	r := New()
	r.Route("/api/v1", func(mux cell.RouteMux) {
		mux.Handle("/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":"pong"}`))
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGroup(t *testing.T) {
	r := New()
	r.Group(func(mux cell.RouteMux) {
		mux.Handle("/grouped", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/grouped", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMount(t *testing.T) {
	r := New()
	subRouter := chi.NewRouter()
	subRouter.Get("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Mount("/sub", subRouter)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub/hello", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouterChain_WebSocketUpgrade(t *testing.T) {
	// Minimal handler that only accepts the WebSocket upgrade.
	// This tests the router middleware chain (security headers, request-ID,
	// logging, recovery) does not interfere with the HTTP→WS handshake.
	// Hub registration is an adapter concern tested in adapters/websocket.
	upgrader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) //nolint:staticcheck
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.CloseNow() //nolint:staticcheck
	})

	r := New()
	r.Mount("/ws", upgrader)

	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil) //nolint:staticcheck
	require.NoError(t, err, "WebSocket upgrade through router middleware chain must succeed")
	conn.CloseNow() //nolint:staticcheck
}

func TestPanicRequestRecordedInAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	r := New()
	r.Handle("/boom", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("access log panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	entry, found := findAccessLogEntry(buf.Bytes(), "/boom")
	require.True(t, found, "access log entry must exist for panic request")
	assert.Equal(t, float64(500), entry["status"], "access log must capture status 500 for panic requests")
}

func TestPanicRequestRecordedInMetrics(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	r := New(WithMetricsCollector(mc))
	r.Handle("/boom", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("metrics panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap := mc.Snapshot()
	key := "GET /boom 500"
	assert.Equal(t, int64(1), snap.RequestCounts[key], "metrics must record panic request as status 500")
}

func TestNormalRequestUnchanged(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	r := New(WithMetricsCollector(mc))
	r.Handle("/ok", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"data":"ok"}`, rec.Body.String())

	// Verify access log has status 200.
	entry, found := findAccessLogEntry(buf.Bytes(), "/ok")
	require.True(t, found, "access log entry must exist")
	assert.Equal(t, float64(200), entry["status"])

	// Verify metrics recorded status 200.
	snap := mc.Snapshot()
	key := "GET /ok 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestNewForListener_AccessLogIncludesListener(t *testing.T) {
	tests := []struct {
		name string
		ref  cell.ListenerRef
	}{
		{name: "primary", ref: cell.PrimaryListener},
		{name: "internal", ref: cell.InternalListener},
		{name: "health", ref: cell.HealthListener},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			original := slog.Default()
			slog.SetDefault(logger)
			defer slog.SetDefault(original)

			r, err := NewForListener(tt.ref)
			require.NoError(t, err)
			r.Handle("/listener-log", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/listener-log", nil)
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusNoContent, rec.Code)
			entry, found := findAccessLogEntry(buf.Bytes(), "/listener-log")
			require.True(t, found, "access log entry must exist")
			assert.Equal(t, tt.ref.String(), entry["listener"])
		})
	}
}

func TestDefaultMiddlewareApplied(t *testing.T) {
	r := New()
	r.Handle("/mid-test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mid-test", nil)
	r.ServeHTTP(rec, req)

	// Security headers should be set by default middleware.
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	// RequestID middleware should set X-Request-Id.
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
}

// --- Trusted proxy fail-fast validation ---

func TestNewE_InvalidTrustedProxies_ReturnsError(t *testing.T) {
	r, err := NewE(WithTrustedProxies([]string{"not-an-ip"}))
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "not-an-ip")
	assert.Contains(t, err.Error(), "router")
}

func TestNewE_ValidTrustedProxies(t *testing.T) {
	r, err := NewE(WithTrustedProxies([]string{"192.168.1.1", "10.0.0.0/8"}))
	require.NoError(t, err)
	assert.NotNil(t, r)
}

func TestNewE_NilTrustedProxies(t *testing.T) {
	r, err := NewE(WithTrustedProxies(nil))
	require.NoError(t, err)
	assert.NotNil(t, r)
}

func TestNew_InvalidTrustedProxies_Panics(t *testing.T) {
	// New is the panic-wrapper over NewE — convenience for non-bootstrap callers.
	assert.Panics(t, func() {
		New(WithTrustedProxies([]string{"not-an-ip"}))
	}, "router.New must panic when trusted proxies contain an invalid entry")
}

func TestNew_InvalidTrustedProxies_PanicMessage(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r)
		msg := fmt.Sprintf("%v", r)
		assert.Contains(t, msg, "not-an-ip")
		assert.Contains(t, msg, "router")
	}()
	New(WithTrustedProxies([]string{"192.168.1.1", "not-an-ip"}))
}

func TestNew_PanicPreservesErrorChain(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r)
		err, ok := r.(error)
		require.True(t, ok, "panic value must be an error, got %T", r)
		assert.ErrorContains(t, err, "router")
	}()
	New(WithTrustedProxies([]string{"not-an-ip"}))
}

// --- Tracing wiring ---

func TestWithTracer_TracingMiddlewareActive(t *testing.T) {
	tracer := tracing.NewTracer("test-router-tracer")
	r := New(WithTracer(tracer))

	var gotTraceID string
	r.Handle("/traced", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tid, ok := ctxkeys.TraceIDFrom(req.Context())
		if ok {
			gotTraceID = tid
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/traced", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, gotTraceID, "trace_id must be set in context when WithTracer is provided")
}

func TestWithTracer_InternalContractRouteTraced(t *testing.T) {
	tracer := &routerSpyTracer{}
	r := New(WithTracer(tracer))

	auth.MustMount(r, auth.Route{
		Contract: testHTTPContract(http.MethodGet, "/internal/v1/rbac/check"),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/rbac/check", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	span := tracer.only(t)
	assert.Equal(t, "test:GET:/internal/v1/rbac/check", span.Attr("gocell.contract.id"))
	assert.Equal(t, "/internal/v1/rbac/check", span.Attr("http.route"))
	assert.Equal(t, int64(http.StatusNoContent), span.Attr("http.status_code"))
}

func TestWithTracer_ExtractsUpstreamTraceparent(t *testing.T) {
	tracer := tracing.NewTracer("test-router-tracer")
	r := New(WithTracer(tracer))

	var gotTraceID string
	r.Handle("/traced-upstream", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var ok bool
		gotTraceID, ok = ctxkeys.TraceIDFrom(req.Context())
		require.True(t, ok)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/traced-upstream", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTraceID,
		"router tracing chain should preserve upstream trace continuity")
}

func TestWithTracingOptions_PublicEndpointNewRoot(t *testing.T) {
	tracer := tracing.NewTracer("test-public")
	r := New(
		WithTracer(tracer),
		WithTracingOptions(middleware.WithPublicEndpointFn(func(req *http.Request) bool {
			return req.URL.Path == "/public"
		})),
	)

	var publicTraceID, internalTraceID string
	r.Handle("/public", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		publicTraceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r.Handle("/internal", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		internalTraceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))

	upstreamTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"

	// Public endpoint: should NOT inherit upstream trace.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("traceparent", "00-"+upstreamTraceID+"-00f067aa0ba902b7-01")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEqual(t, upstreamTraceID, publicTraceID,
		"public endpoint must NOT inherit upstream trace (new root)")

	// Internal endpoint: should inherit upstream trace.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("traceparent", "00-"+upstreamTraceID+"-00f067aa0ba902b7-01")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, upstreamTraceID, internalTraceID,
		"internal endpoint must inherit upstream trace")
}

func TestNoTracer_NoTraceID(t *testing.T) {
	r := New() // no WithTracer

	var hasTraceID bool
	r.Handle("/no-trace", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, hasTraceID = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/no-trace", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, hasTraceID, "trace_id must not be set when no tracer is configured")
}

func TestWithTracer_TraceIDInAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	tracer := tracing.NewTracer("log-test")
	r := New(WithTracer(tracer))
	r.Handle("/log-trace", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/log-trace", nil)
	r.ServeHTTP(rec, req)

	// Parse the access log entry and check for trace_id.
	entry, found := findAccessLogEntry(buf.Bytes(), "/log-trace")
	require.True(t, found, "access log entry must exist")
	assert.NotEmpty(t, entry["trace_id"], "access log must include trace_id when tracing is configured")
}

func TestAccessLog_IncludesRealIP(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	r := New(WithTrustedProxies([]string{"127.0.0.1"}))
	r.Handle("/real-ip-test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/real-ip-test", nil)
	req.RemoteAddr = "127.0.0.1:12345" // trusted proxy
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse all JSON log entries and find the access log for this request.
	entry, found := findAccessLogEntry(buf.Bytes(), "/real-ip-test")
	require.True(t, found, "access log entry must exist")
	assert.Equal(t, "203.0.113.50", entry["real_ip"],
		"access log must include real_ip extracted from X-Forwarded-For")
}

func TestWithTracer_PanicRequestTraced(t *testing.T) {
	tracer := tracing.NewTracer("panic-trace-test")
	r := New(WithTracer(tracer))
	r.Handle("/boom-traced", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("tracing panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom-traced", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"recovery must still work with tracing in chain")
}

func TestWithTracer_PanicRequestRecordedInMetrics(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	tracer := tracing.NewTracer("metrics-panic-test")
	r := New(WithTracer(tracer), WithMetricsCollector(mc))
	r.Handle("/boom-full", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("full chain panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom-full", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	snap := mc.Snapshot()
	key := "GET /boom-full 500"
	assert.Equal(t, int64(1), snap.RequestCounts[key],
		"metrics must record panic request as 500 even with tracing in chain")
}

// --- Rate limiter wiring ---

// routerTestLimiter is a minimal RateLimiter for router integration tests.
type routerTestLimiter struct {
	allow bool
	keys  []string
}

func (l *routerTestLimiter) Allow(key string) bool {
	l.keys = append(l.keys, key)
	return l.allow
}

func TestWithRateLimiter_InDefaultChain(t *testing.T) {
	limiter := &routerTestLimiter{allow: true}
	r := New(WithRateLimiter(limiter))
	r.Handle("/rl-test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rl-test", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, limiter.keys, "rate limiter must be invoked in default chain")
}

func TestWithRateLimiter_Rejected_Returns429(t *testing.T) {
	limiter := &routerTestLimiter{allow: false}
	r := New(WithRateLimiter(limiter))
	r.Handle("/rl-reject", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called when rate limited")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rl-reject", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_RATE_LIMITED", errObj["code"])
}

func TestWithTracer_RateLimitedContractRouteTagged(t *testing.T) {
	limiter := &routerTestLimiter{allow: false}
	tracer := &routerSpyTracer{}
	r := New(WithRateLimiter(limiter), WithTracer(tracer))
	auth.MustMount(r, auth.Route{
		Contract: testHTTPContract(http.MethodGet, "/api/v1/rl-contract/{id}"),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler should not be called when rate limited")
		}),
		Public: true,
	})
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/rl-contract/123", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	span := tracer.only(t)
	assert.Equal(t, "test:GET:/api/v1/rl-contract/{id}", span.Attr("gocell.contract.id"))
	assert.Equal(t, "/api/v1/rl-contract/{id}", span.Attr("http.route"))
	assert.Equal(t, int64(http.StatusTooManyRequests), span.Attr("http.status_code"))
}

// --- Circuit breaker wiring ---

// routerTestBreaker is a minimal Allower for router integration tests.
type routerTestBreaker struct {
	allowErr error
	called   bool
}

func (b *routerTestBreaker) Allow() (bool, func(error)) {
	b.called = true
	if b.allowErr != nil {
		return false, nil
	}
	return true, func(error) {}
}

func TestWithCircuitBreaker_InDefaultChain(t *testing.T) {
	breaker := &routerTestBreaker{}
	r := New(WithCircuitBreaker(breaker))
	r.Handle("/cb-test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cb-test", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, breaker.called, "circuit breaker must be invoked in default chain")
}

func TestWithCircuitBreaker_Open_Returns503(t *testing.T) {
	breaker := &routerTestBreaker{allowErr: fmt.Errorf("circuit breaker is open")}
	r := New(WithCircuitBreaker(breaker))
	r.Handle("/cb-reject", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called when circuit is open")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cb-reject", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_CIRCUIT_OPEN", errObj["code"])
}

func TestWithCircuitBreaker_NilInterface_Error(t *testing.T) {
	// A bare nil interface value must cause NewE to return an error so that
	// Bootstrap.Run fails fast instead of silently skipping CB protection.
	_, err := NewE(WithCircuitBreaker(nil))
	require.Error(t, err, "nil interface Allower must return error from NewE")
	assert.Contains(t, err.Error(), "circuit breaker")
}

func TestWithCircuitBreaker_TypedNilPointer_Error(t *testing.T) {
	// A typed-nil (*routerTestBreaker)(nil) must also be rejected: the interface
	// value is non-nil but the underlying pointer is nil, so calling Allow()
	// on it would panic at runtime.
	var cb *routerTestBreaker // typed nil
	_, err := NewE(WithCircuitBreaker(cb))
	require.Error(t, err, "typed-nil Allower must return error from NewE")
	assert.Contains(t, err.Error(), "circuit breaker")
}

// --- Infra endpoints bypass RL/CB ---

func TestInfraEndpoints_BypassRateLimiter(t *testing.T) {
	// PR-A14b: health endpoints live on a dedicated HealthListener router that has
	// no rate limiter configured. Physical isolation guarantees bypass — the primary
	// router (with the rejecting rate limiter) never even sees /healthz requests.
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	// Primary router rejects ALL traffic via rate limiter.
	primaryRtr := New(WithRateLimiter(&routerTestLimiter{allow: false}))
	primaryRtr.Handle("/api/v1/biz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should be rate-limited")
	}))

	// Health router has no rate limiter — /healthz always reachable.
	healthRtr, err := NewForListener(cell.HealthListener)
	require.NoError(t, err)
	healthRtr.Handle("/healthz", hh.LivezHandler())

	// Business route is rate-limited on primary router.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/biz", nil)
	primaryRtr.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// /healthz is reachable on the health router (no rate limiter).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRtr.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code,
		"/healthz must be reachable on HealthListener router even when primary router has rejecting rate limiter")
}

func TestInfraEndpoints_BypassCircuitBreaker(t *testing.T) {
	// PR-A14b: health endpoints live on a dedicated HealthListener router that has
	// no circuit breaker configured. Physical isolation guarantees bypass — the
	// primary router (with the open circuit breaker) never sees /readyz requests.
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	// Primary router rejects ALL traffic via open circuit breaker.
	primaryRtr := New(WithCircuitBreaker(&routerTestBreaker{allowErr: fmt.Errorf("open")}))
	primaryRtr.Handle("/api/v1/biz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should be circuit-broken")
	}))

	// Health router has no circuit breaker — /readyz always reachable.
	healthRtr, err := NewForListener(cell.HealthListener)
	require.NoError(t, err)
	healthRtr.Handle("/readyz", hh.ReadyzHandler())

	// Business route is circuit-broken on primary router.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/biz", nil)
	primaryRtr.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// /readyz is reachable on the health router (no circuit breaker).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	healthRtr.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code,
		"/readyz must be reachable on HealthListener router even when primary router has open circuit breaker")
}

func TestMetrics_Records429And503(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	limiter := &routerTestLimiter{allow: false}
	breaker := &routerTestBreaker{allowErr: fmt.Errorf("open")}
	r := New(
		WithMetricsCollector(mc),
		WithRateLimiter(limiter),
		WithCircuitBreaker(breaker),
	)
	r.Handle("/biz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Request 1: Rate-limited → 429 must be recorded in metrics.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/biz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Request 2: Allow through RL, hit open CB → 503 must be recorded.
	limiter.allow = true
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/biz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	snap := mc.Snapshot()
	found429, found503 := false, false
	for key, count := range snap.RequestCounts {
		if strings.Contains(key, "429") && count > 0 {
			found429 = true
		}
		if strings.Contains(key, "503") && count > 0 {
			found503 = true
		}
	}
	assert.True(t, found429, "metrics must record 429 responses from rate limiter")
	assert.True(t, found503, "metrics must record 503 responses from circuit breaker")
}

// --- Auth middleware wiring ---

// routerTestVerifier is a minimal IntentTokenVerifier for router integration tests.
type routerTestVerifier struct {
	claims auth.Claims
	err    error
}

func (v *routerTestVerifier) Verify(_ context.Context, _ string) (auth.Claims, error) {
	return v.claims, v.err
}

func (v *routerTestVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return v.claims, v.err
}

func TestWithAuthMiddleware_ProtectedRoute_NoToken_Returns401(t *testing.T) {
	verifier := &routerTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	r := New(WithAuthMiddleware(verifier))
	r.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called without auth token")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", errObj["code"])
}

func TestWithAuthMiddleware_ProtectedRoute_ValidToken_Returns200(t *testing.T) {
	verifier := &routerTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	r := New(WithAuthMiddleware(verifier))

	var gotSubject string
	r.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, ok := auth.FromContext(req.Context())
		assert.True(t, ok, "principal must be in context")
		gotSubject = p.Subject
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "user-1", gotSubject)
}

func TestWithAuthMiddleware_PublicEndpoint_SkipsAuth(t *testing.T) {
	// F3: public endpoints are declared via auth.MustMount(Public:true) + FinalizeAuth.
	verifier := &routerTestVerifier{
		err: fmt.Errorf("should not be called"),
	}
	r := New(WithAuthMiddleware(verifier))

	loginHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodPost, "/api/v1/access/sessions/login"), Handler: loginHandler, Public: true})
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"public endpoint must be accessible without auth token")
}

func TestWithAuthMiddleware_InfraEndpoints_BypassAuth(t *testing.T) {
	// PR-A14b: health endpoints live on a dedicated HealthListener router that has
	// no auth middleware. Physical isolation guarantees bypass — the primary router
	// (with auth middleware that would reject all requests) never sees /healthz.
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	verifier := &routerTestVerifier{
		err: fmt.Errorf("all tokens rejected"),
	}
	// Primary router has auth that rejects everything.
	primaryRtr := New(WithAuthMiddleware(verifier))
	primaryRtr.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Health router has no auth — /healthz always reachable.
	healthRtr, err := NewForListener(cell.HealthListener)
	require.NoError(t, err)
	healthRtr.Handle("/healthz", hh.LivezHandler())

	// /api/v1/data requires auth on primary router.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	primaryRtr.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// /healthz is reachable on the health router (no auth middleware).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRtr.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code,
		"/healthz must be reachable on HealthListener router without auth (physical isolation)")
}

func TestWithAuthMiddleware_ChainOrder_RateLimitBeforeAuth(t *testing.T) {
	// Rate limiter rejects all traffic. Auth middleware is also configured.
	// We expect 429 (not 401), proving RL runs before auth in the chain.
	limiter := &routerTestLimiter{allow: false}
	verifier := &routerTestVerifier{
		err: fmt.Errorf("should not be called"),
	}
	r := New(WithRateLimiter(limiter), WithAuthMiddleware(verifier))
	r.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"rate limiter must run before auth middleware — expect 429, not 401")
}

func TestWithAuthMiddleware_InvalidToken_Returns401(t *testing.T) {
	verifier := &routerTestVerifier{
		err: fmt.Errorf("token expired"),
	}
	r := New(WithAuthMiddleware(verifier))
	r.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called with invalid token")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer expired-token")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", errObj["code"])
}

func TestWithAuthMiddleware_NilVerifier_Panics(t *testing.T) {
	assert.Panics(t, func() {
		WithAuthMiddleware(nil)
	}, "WithAuthMiddleware must panic when verifier is nil")
}

func TestWithRequestIDOptions_PublicEndpoint(t *testing.T) {
	r := New(
		WithRequestIDOptions(middleware.WithReqIDPublicEndpointFn(func(req *http.Request) bool {
			return req.URL.Path == "/public"
		})),
	)

	var publicID, internalID string
	r.Handle("/public", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		publicID, _ = ctxkeys.RequestIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r.Handle("/internal", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		internalID, _ = ctxkeys.RequestIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// Public endpoint: must ignore client-supplied header.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("X-Request-Id", "attacker-id")
	r.ServeHTTP(rec, req)
	assert.NotEqual(t, "attacker-id", publicID,
		"public endpoint must reject client-supplied X-Request-Id")
	assert.Len(t, publicID, 36, "public endpoint must generate fresh UUID")

	// Non-public endpoint: must accept valid client-supplied header.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Request-Id", "trusted-upstream-id")
	r.ServeHTTP(rec, req)
	assert.Equal(t, "trusted-upstream-id", internalID,
		"non-public endpoint must accept trusted upstream X-Request-Id")
}

// ---------------------------------------------------------------------------
// F3 auth.Mount + FinalizeAuth trust-boundary tests
// ---------------------------------------------------------------------------

func TestDeclareAuth_AuthBypass(t *testing.T) {
	// F3: public routes declared via auth.MustMount(Public:true) bypass JWT check.
	verifier := &routerTestVerifier{claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}}}
	r := New(WithAuthMiddleware(verifier))

	var reached bool
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/auth/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }), Public: true})
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	r.ServeHTTP(rec, req)

	assert.True(t, reached, "public endpoint must bypass auth and reach handler")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDeclareAuth_AuthBypass_MethodMismatch_Returns401(t *testing.T) {
	// POST /api/v1/auth/login is public; GET must still require auth.
	verifier := &routerTestVerifier{err: fmt.Errorf("should not be called")}
	r := New(WithAuthMiddleware(verifier))

	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodPost, "/api/v1/auth/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Public: true})
	// Register GET with a policy so it is covered by policy enforcement;
	// the verifier always fails so a GET without a token still returns 401.
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/auth/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("GET must not bypass auth when only POST is declared public")
	}), Policy: authtest.RequireAuthenticated()})
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"GET must be rejected when only POST /api/v1/auth/login is declared public")
}

func TestDeclareAuth_TracingNewRoot(t *testing.T) {
	// F3: public routes declared via auth.Mount create new trace roots.
	// PR-A14a: /internal is whitelisted from policy coverage (raw r.Handle)
	// so the non-public route runs without an auth gate.
	tracer := tracing.NewTracer("test-combined")
	r := New(
		WithTracer(tracer),
		WithPolicyCoverageWhitelist([]string{"/internal/*"}),
	)

	var publicTraceID, internalTraceID string
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/public"), Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		publicTraceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}), Public: true})
	r.Handle("/internal", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		internalTraceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, r.FinalizeAuth())

	upstreamTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	tp := "00-" + upstreamTraceID + "-00f067aa0ba902b7-01"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("traceparent", tp)
	r.ServeHTTP(rec, req)
	assert.NotEqual(t, upstreamTraceID, publicTraceID,
		"F3 declared public endpoint must create new trace root")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("traceparent", tp)
	r.ServeHTTP(rec, req)
	assert.Equal(t, upstreamTraceID, internalTraceID,
		"non-public endpoint must inherit upstream trace")
}

func TestDeclareAuth_RequestIDRejectsClient(t *testing.T) {
	// F3: public routes reject client-supplied X-Request-Id.
	// PR-A14a: /internal is whitelisted from policy coverage (raw r.Handle)
	// so the non-public route runs without an auth gate.
	r := New(WithPolicyCoverageWhitelist([]string{"/internal/*"}))

	var publicID, internalID string
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/public"), Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		publicID, _ = ctxkeys.RequestIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}), Public: true})
	r.Handle("/internal", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		internalID, _ = ctxkeys.RequestIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("X-Request-Id", "attacker-id")
	r.ServeHTTP(rec, req)
	assert.NotEqual(t, "attacker-id", publicID,
		"F3 declared public endpoint must reject client-supplied request ID")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/internal", nil)
	req.Header.Set("X-Request-Id", "trusted-upstream-id")
	r.ServeHTTP(rec, req)
	assert.Equal(t, "trusted-upstream-id", internalID,
		"non-public endpoint must accept trusted upstream ID")
}

func TestDeclareAuth_ProtectedStillRequiresAuth(t *testing.T) {
	// F3: only declared public routes bypass auth; others still require a token.
	verifier := &routerTestVerifier{claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}}}
	r := New(WithAuthMiddleware(verifier))

	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/auth/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Public: true})
	// /api/v1/data is protected — declared with a policy to satisfy coverage enforcement.
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/data"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Policy: authtest.RequireAuthenticated()})
	require.NoError(t, r.FinalizeAuth())

	// Protected endpoint without token → 401.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"non-public endpoint must still require auth when other routes are declared public")
}

func TestDeclareAuth_UserTracingOptions_FineGrained(t *testing.T) {
	// F3 lazy-binding: WithTracingOptions (user-supplied, appended after prepend)
	// wins for tracing (last-write-wins in tracingConfig). The lazy closure from
	// auth.Mount / FinalizeAuth is consulted for auth + RequestID; the explicit
	// WithTracingOptions fn controls trace root creation.
	tracer := tracing.NewTracer("test-combined-fine")
	r := New(
		WithTracer(tracer),
		WithTracingOptions(middleware.WithPublicEndpointFn(func(req *http.Request) bool {
			return req.URL.Path == "/fine-grained-public"
		})),
		WithPolicyCoverageWhitelist([]string{"/fine-grained-public/*"}),
	)

	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/public"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), Public: true})
	// PR-A14a: /fine-grained-public is whitelisted + registered raw (non-public).
	r.Handle("/fine-grained-public", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	require.NoError(t, r.FinalizeAuth())

	var publicTraceID, fineTraceID string
	// Re-register with trace capture (FinalizeAuth already called; use r.Handle for non-declared routes).
	// Instead, rebuild using a fresh router that captures trace IDs inline.
	r2 := New(
		WithTracer(tracer),
		WithTracingOptions(middleware.WithPublicEndpointFn(func(req *http.Request) bool {
			return req.URL.Path == "/fine-grained-public"
		})),
		WithPolicyCoverageWhitelist([]string{"/fine-grained-public/*"}),
	)
	auth.MustMount(r2, auth.Route{Contract: testHTTPContract(http.MethodGet, "/public"), Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		publicTraceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}), Public: true})
	// PR-A14a: /fine-grained-public is whitelisted + registered raw (non-public).
	r2.Handle("/fine-grained-public", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fineTraceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, r2.FinalizeAuth())

	upstreamTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	tp := "00-" + upstreamTraceID + "-00f067aa0ba902b7-01"

	// /public: user fn returns false (only matches /fine-grained-public) →
	// user fn wins (last-write-wins) → NOT a public endpoint for tracing →
	// inherits upstream trace.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("traceparent", tp)
	r2.ServeHTTP(rec, req)
	assert.Equal(t, upstreamTraceID, publicTraceID,
		"F3: user WithTracingOptions wins for tracing (lazy prepend, user appended last)")

	// /fine-grained-public: user fn returns true → IS public for tracing →
	// new trace root (does not inherit upstream).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/fine-grained-public", nil)
	req.Header.Set("traceparent", tp)
	r2.ServeHTTP(rec, req)
	assert.NotEqual(t, upstreamTraceID, fineTraceID,
		"F3: user-supplied fine-grained fn still creates new trace root for its paths")
}

// ---------------------------------------------------------------------------
// WithSecurityHeadersOptions wiring test (F1-ARCH-03)
// ---------------------------------------------------------------------------

func TestWithSecurityHeadersOptions_CustomHSTS(t *testing.T) {
	r := New(
		WithSecurityHeadersOptions(
			middleware.WithHSTSIncludeSubDomains(),
			middleware.WithHSTSPreload(),
		),
	)
	r.Handle("/hsts-test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hsts-test", nil)
	r.ServeHTTP(rec, req)

	hsts := rec.Header().Get("Strict-Transport-Security")
	assert.Contains(t, hsts, "includeSubDomains",
		"custom HSTS option must reach SecurityHeaders middleware")
	assert.Contains(t, hsts, "preload",
		"custom HSTS option must reach SecurityHeaders middleware")
}

func TestWithSecurityHeadersOptions_DefaultHSTS(t *testing.T) {
	r := New()
	r.Handle("/default-hsts", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/default-hsts", nil)
	r.ServeHTTP(rec, req)

	hsts := rec.Header().Get("Strict-Transport-Security")
	assert.NotEmpty(t, hsts, "default SecurityHeaders must set HSTS")
	assert.NotContains(t, hsts, "preload",
		"default HSTS must not include preload unless opted in")
}

// ---------------------------------------------------------------------------
// F3 auth.Mount edge cases (F3-TEST-01, F3-TEST-02)
// ---------------------------------------------------------------------------

func TestDeclareAuth_NoPublicDecls_TracingUnchanged(t *testing.T) {
	// When no routes are declared public, tracing inherits upstream trace as normal.
	// PR-A14a: /test is whitelisted from policy coverage (raw r.Handle without
	// auth.Mount) so the route runs without any auth gate and the tracing
	// context is captured unconditionally.
	tracer := tracing.NewTracer("test-empty")
	r := New(
		WithTracer(tracer),
		WithPolicyCoverageWhitelist([]string{"/test/*"}),
	)

	var traceID string
	r.Handle("/test", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		traceID, _ = ctxkeys.TraceIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, r.FinalizeAuth())

	upstreamTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("traceparent", "00-"+upstreamTraceID+"-00f067aa0ba902b7-01")
	r.ServeHTTP(rec, req)

	assert.Equal(t, upstreamTraceID, traceID,
		"no declared public routes must not alter tracing behavior")
}

func TestDeclareAuth_PathNormalization(t *testing.T) {
	// auth.Mount normalises paths via path.Clean: "/api/v1//login" → "/api/v1/login".
	r := New()

	var gotID string
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1//login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotID, _ = ctxkeys.RequestIDFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	}), Public: true})
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/login", nil)
	req.Header.Set("X-Request-Id", "attacker-id")
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, "attacker-id", gotID,
		"path.Clean must normalize /api/v1//login to /api/v1/login for matching")
}

func TestDeclareAuth_MethodAware_GETDoesNotBypassForPOSTOnly(t *testing.T) {
	// POST /api/v1/auth/login is the only public endpoint.
	// GET requests to the same path must still require auth.
	verifier := &routerTestVerifier{
		claims: auth.Claims{Subject: "user-1"},
	}
	r := New(WithAuthMiddleware(verifier))

	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodPost, "/api/v1/auth/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Public: true})
	// Register the GET handler with a policy so it is covered by policy enforcement;
	// auth middleware will require a valid token for GET.
	auth.MustMount(r, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/auth/login"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Policy: authtest.RequireAuthenticated()})
	require.NoError(t, r.FinalizeAuth())

	// POST without token → public, 200.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "POST must bypass auth (declared public)")

	// GET without token → not public, 401.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"GET must require auth when only POST is declared public")
}

// TestRouter_ServeHTTP_NoFinalizeAuth_Panics asserts that calling ServeHTTP
// on a router that has auth route metadata declared but FinalizeAuth has NOT
// been called results in a panic with the expected message.
//
// This is the safety guard that prevents a mis-wired bootstrap from silently
// skipping the auth compilation step (FinalizeAuth) and serving requests
// without the compiled public/PasswordResetExempt matchers in place.
func TestRouter_ServeHTTP_NoFinalizeAuth_Panics(t *testing.T) {
	r := New()

	// Declare auth metadata without calling FinalizeAuth — this is the
	// mis-wired state the guard is designed to detect.
	r.DeclareAuthMeta(cell.AuthRouteMeta{
		Method: "GET",
		Path:   "/api/v1/probe",
		Public: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
	rec := httptest.NewRecorder()

	assert.PanicsWithValue(t,
		"router: FinalizeAuth must be called before ServeHTTP when auth route metadata has been declared",
		func() { r.ServeHTTP(rec, req) },
		"ServeHTTP must panic when FinalizeAuth has not been called after DeclareAuthMeta",
	)
}
