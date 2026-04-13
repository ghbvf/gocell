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
	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCell struct{ *cell.BaseCell }

func newStubCell(id string) *stubCell {
	return &stubCell{BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore})}
}

func TestRouterImplementsRouteMux(t *testing.T) {
	r := New()
	var mux cell.RouteMux = r
	assert.NotNil(t, mux)
}

func TestHealthEndpoints(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	r := New(WithHealthHandler(hh))

	// Test /healthz
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])

	// Test /readyz
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMetricsEndpoint(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	r := New(
		WithMetricsCollector(mc),
		WithMetricsHandler(mc.Handler()),
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.ServeHTTP(rec, req)
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
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.CloseNow()
	})

	r := New()
	r.Mount("/ws", upgrader)

	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err, "WebSocket upgrade through router middleware chain must succeed")
	conn.CloseNow()
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

	// Parse all JSON log entries and find the access log (level=INFO, msg="http request").
	var found bool
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] == "http request" {
			found = true
			assert.Equal(t, float64(500), entry["status"], "access log must capture status 500 for panic requests")
			break
		}
	}
	assert.True(t, found, "access log entry must exist for panic request")
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
	var found bool
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] == "http request" {
			found = true
			assert.Equal(t, float64(200), entry["status"])
			break
		}
	}
	assert.True(t, found, "access log entry must exist")

	// Verify metrics recorded status 200.
	snap := mc.Snapshot()
	key := "GET /ok 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
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
	var found bool
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] == "http request" {
			found = true
			assert.NotEmpty(t, entry["trace_id"], "access log must include trace_id when tracing is configured")
			break
		}
	}
	assert.True(t, found, "access log entry must exist")
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

// --- Circuit breaker wiring ---

// routerTestBreaker is a minimal CircuitBreakerPolicy for router integration tests.
type routerTestBreaker struct {
	allowErr error
	called   bool
}

func (b *routerTestBreaker) Allow() (func(bool), error) {
	b.called = true
	if b.allowErr != nil {
		return nil, b.allowErr
	}
	return func(bool) {}, nil
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

// --- Infra endpoints bypass RL/CB ---

func TestInfraEndpoints_BypassRateLimiter(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	limiter := &routerTestLimiter{allow: false} // reject ALL business traffic
	r := New(WithHealthHandler(hh), WithRateLimiter(limiter))

	// /healthz must bypass rate limiter and return 200.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"/healthz must be reachable even when rate limiter rejects all traffic")
}

func TestInfraEndpoints_BypassCircuitBreaker(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	breaker := &routerTestBreaker{allowErr: fmt.Errorf("open")} // reject ALL
	r := New(WithHealthHandler(hh), WithCircuitBreaker(breaker))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"/readyz must be reachable even when circuit breaker is open")
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

// routerTestVerifier is a minimal TokenVerifier for router integration tests.
type routerTestVerifier struct {
	claims auth.Claims
	err    error
}

func (v *routerTestVerifier) Verify(_ context.Context, _ string) (auth.Claims, error) {
	return v.claims, v.err
}

func TestWithAuthMiddleware_ProtectedRoute_NoToken_Returns401(t *testing.T) {
	verifier := &routerTestVerifier{
		claims: auth.Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	r := New(WithAuthMiddleware(verifier, nil))
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
	r := New(WithAuthMiddleware(verifier, nil))

	var gotSubject string
	r.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		claims, ok := auth.ClaimsFrom(req.Context())
		assert.True(t, ok, "claims must be in context")
		gotSubject = claims.Subject
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
	verifier := &routerTestVerifier{
		err: fmt.Errorf("should not be called"),
	}
	publicPaths := []string{"/api/v1/access/sessions/login"}
	r := New(WithAuthMiddleware(verifier, publicPaths))

	r.Handle("/api/v1/access/sessions/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"public endpoint must be accessible without auth token")
}

func TestWithAuthMiddleware_InfraEndpoints_BypassAuth(t *testing.T) {
	// Auth middleware is on mux (business routes). Infra endpoints (/healthz, /readyz)
	// are on outerMux and naturally bypass mux-level auth.
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	verifier := &routerTestVerifier{
		err: fmt.Errorf("should not be called for infra"),
	}
	r := New(WithHealthHandler(hh), WithAuthMiddleware(verifier, nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"/healthz must be reachable without auth token (infra on outerMux)")
}

func TestWithAuthMiddleware_ChainOrder_RateLimitBeforeAuth(t *testing.T) {
	// Rate limiter rejects all traffic. Auth middleware is also configured.
	// We expect 429 (not 401), proving RL runs before auth in the chain.
	limiter := &routerTestLimiter{allow: false}
	verifier := &routerTestVerifier{
		err: fmt.Errorf("should not be called"),
	}
	r := New(WithRateLimiter(limiter), WithAuthMiddleware(verifier, nil))
	r.Handle("/api/v1/data", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"rate limiter must run before auth middleware — expect 429, not 401")
}
