//go:build integration

package transport_test

// remote_integration_test.go — integration tests for RemoteHTTPTransport.
//
// These tests verify the full remote transport stack against a real TCP
// loopback server with a real auth.ServiceTokenMiddleware chain.
//
// No Docker or external services are required: all I/O is against 127.0.0.1
// using httptest.NewServer. The tests run with -tags=integration.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// ---- helpers ----------------------------------------------------------------

// testTenantID is a canonical UUID used across integration tests.
const testTenantIDStr = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

// t4DeadlineTimeout is the tiny per-request deadline T4 uses to force a
// context-deadline-exceeded against a server that never responds (TEST-TIME-LITERAL-01).
const t4DeadlineTimeout = 1 * time.Millisecond

// testTenantID parses the shared test tenant ID (panics if invalid — a test
// helper invoked at test init, not in production).
func mustTenantID(t *testing.T) tenant.TenantID {
	t.Helper()
	tid, err := tenant.ParseTenantID(testTenantIDStr)
	if err != nil {
		t.Fatalf("ParseTenantID: %v", err)
	}
	return tid
}

// mustRing builds a test HMAC ring.
func mustRing(t *testing.T) *auth.HMACKeyRing {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-32-bytes-long-xxxxx"), nil)
	if err != nil {
		t.Fatalf("NewHMACKeyRing: %v", err)
	}
	return ring
}

// mustNonceStore builds an in-memory nonce store.
func mustNonceStore(t *testing.T) auth.NonceStore {
	t.Helper()
	ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	if err != nil {
		t.Fatalf("NewInMemoryNonceStore: %v", err)
	}
	return ns
}

// buildServer starts a test HTTP server behind a real ServiceToken middleware
// chain with a RequireCallerCell guard for "accesscore". The business handler
// returns a JSON body {"data":{"ok":true}} with status 200.
func buildServer(t *testing.T, ring *auth.HMACKeyRing, clk clock.Clock, ns auth.NonceStore) *httptest.Server {
	t.Helper()

	biz := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	})
	guarded := auth.ServiceTokenMiddleware(ring, clk, auth.WithServiceTokenNonceStore(ns))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := auth.RequireCallerCell("accesscore")(r); err != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			biz.ServeHTTP(w, r)
		}),
	)

	srv := httptest.NewServer(guarded)
	t.Cleanup(srv.Close)
	return srv
}

// unsignedReq builds a GET request without signing, for tests that sign inline
// with a specific caller-cell literal (T7).
func unsignedReq(t *testing.T, method, urlStr string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, urlStr, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

// signedReq builds a GET request and signs it as caller-cell "accesscore" (the
// happy-path caller). The cell id is an inline string literal so the
// SVCTOKEN-CALLER-CELL-REQUIRED-01 B-arm can statically verify it; tests that
// need a different caller (T7's 403 path) inline their own SignInternalRequest.
func signedReq(t *testing.T, method, urlStr string, ring *auth.HMACKeyRing, tid tenant.TenantID, clk clock.Clock) *http.Request {
	t.Helper()
	req := unsignedReq(t, method, urlStr)
	if err := auth.SignInternalRequest(context.Background(), ring, "accesscore", req, tid, clk); err != nil {
		t.Fatalf("SignInternalRequest: %v", err)
	}
	return req
}

// countingMetrics is a minimal metrics helper for integration tests.
type countingMetrics struct {
	mu     sync.Mutex
	counts map[string]int
}

func newCountingMetrics(t *testing.T) (*transport.Metrics, *countingMetrics) {
	t.Helper()
	cm := &countingMetrics{counts: map[string]int{}}
	m, err := transport.NewMetrics(&countingProvider{cm: cm})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return m, cm
}

func (cm *countingMetrics) count(mode string) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.counts[mode]
}

// countingProvider implements kernelmetrics.Provider for integration tests.
type countingProvider struct {
	cm *countingMetrics
}

func (cp *countingProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	base, err := kernelmetrics.NopProvider{}.CounterVec(opts)
	if err != nil {
		return nil, err
	}
	return &countingVec{base: base, cm: cp.cm}, nil
}

func (cp *countingProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return kernelmetrics.NopProvider{}.HistogramVec(opts)
}

func (cp *countingProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return kernelmetrics.NopProvider{}.GaugeVec(opts)
}
func (cp *countingProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

type countingVec struct {
	base kernelmetrics.CounterVec
	cm   *countingMetrics
}

func (cv *countingVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	return &countingCounter{cm: cv.cm, mode: l["transport_mode"]}
}

func (cv *countingVec) Registered() bool { return true }

type countingCounter struct {
	cm   *countingMetrics
	mode string
}

func (c *countingCounter) Inc(_ context.Context) {
	c.cm.mu.Lock()
	defer c.cm.mu.Unlock()
	c.cm.counts[c.mode]++
}
func (c *countingCounter) Add(_ context.Context, _ float64) {}

// buildTransport constructs a RemoteHTTPTransport pointing at the given server URL.
func buildTransport(t *testing.T, serverURL string, m *transport.Metrics) *transport.RemoteHTTPTransport {
	t.Helper()
	resolver := transport.NewStaticResolver(map[string]string{"configcore": serverURL})
	return transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, &http.Client{}, m, nil)
}

// ---- tests ------------------------------------------------------------------

// T1: happy path — correctly signed request → 200, metric incremented.
func TestRemoteIntegration_T1_Happy(t *testing.T) {
	t.Parallel()

	ring := mustRing(t)
	ns := mustNonceStore(t)
	clk := clock.Real()
	tid := mustTenantID(t)
	srv := buildServer(t, ring, clk, ns)

	m, cm := newCountingMetrics(t)
	tr := buildTransport(t, srv.URL, m)

	const path = "/internal/v1/config/app.name"
	req := signedReq(t, http.MethodGet, "http://ignored"+path, ring, tid, clk)

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := cm.count("remote"); got != 1 {
		t.Errorf("cell_transport_requests_total{transport_mode=remote} = %d, want 1", got)
	}
}

// T2: URL rewrite + header preservation — server receives correct path and
// Authorization header.
func TestRemoteIntegration_T2_URLRewritePreservesHeaders(t *testing.T) {
	t.Parallel()

	ring := mustRing(t)
	clk := clock.Real()
	tid := mustTenantID(t)

	var (
		gotPath string
		gotAuth string
		gotTid  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotTid = r.Header.Get(auth.HeaderTenantID)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	const wantPath = "/internal/v1/config/my.key"
	req := signedReq(t, http.MethodGet, "http://in-proc-ignored"+wantPath, ring, tid, clk)

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer resp.Body.Close()

	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth == "" {
		t.Error("Authorization header missing on server side")
	}
	if gotTid != testTenantIDStr {
		t.Errorf("X-Tenant-ID = %q, want %q", gotTid, testTenantIDStr)
	}
}

// T3: connection refused → KindUnavailable / ErrUpstreamCellUnavailable, no metric.
func TestRemoteIntegration_T3_ConnectionRefused(t *testing.T) {
	t.Parallel()

	m, cm := newCountingMetrics(t)

	// Use a closed server to provoke connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	addr := srv.URL
	srv.Close()

	resolver := transport.NewStaticResolver(map[string]string{"configcore": addr})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, &http.Client{}, m, nil)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/x", nil)
	_, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindUnavailable {
		t.Errorf("Kind = %v, want KindUnavailable", ec.Kind)
	}
	if ec.Code != errcode.ErrUpstreamCellUnavailable {
		t.Errorf("Code = %v, want ErrUpstreamCellUnavailable", ec.Code)
	}
	if got := cm.count("remote"); got != 0 {
		t.Errorf("metric must NOT be recorded on dial failure, got count=%d", got)
	}
}

// T4: ctx deadline exceeded → KindUnavailable.
func TestRemoteIntegration_T4_CtxDeadline(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		srv.Close()
	})

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), t4DeadlineTimeout)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://ignored/x", nil)
	_, err := tr.DoContract(ctx, "http.config.internal.get.v1", req)
	if err == nil {
		t.Fatal("expected error for deadline exceeded, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindUnavailable {
		t.Errorf("Kind = %v, want KindUnavailable", ec.Kind)
	}
}

// T5: server returns 503 → DoContract returns (resp, nil) with StatusCode 503.
func TestRemoteIntegration_T5_5xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/x", nil)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract returned error for 5xx — want (resp, nil): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", resp.StatusCode)
	}
}

// T6: wrong ring → real middleware returns 401.
func TestRemoteIntegration_T6_WrongRing_401(t *testing.T) {
	t.Parallel()

	ring := mustRing(t)
	ns := mustNonceStore(t)
	clk := clock.Real()
	tid := mustTenantID(t)
	srv := buildServer(t, ring, clk, ns)

	// Use a DIFFERENT ring to sign — server will reject it.
	wrongRing, _ := auth.NewHMACKeyRing([]byte("wrong-key-32-bytes-long-xxxxxxxxx"), nil)
	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	req := signedReq(t, http.MethodGet, "http://ignored/internal/v1/x", wrongRing, tid, clk)

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v (want nil error + 401 response)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", resp.StatusCode)
	}
}

// T7: correct ring but callerCell not in allowlist → server returns 403.
func TestRemoteIntegration_T7_WrongCallerCell_403(t *testing.T) {
	t.Parallel()

	ring := mustRing(t)
	ns := mustNonceStore(t)
	clk := clock.Real()
	tid := mustTenantID(t)
	srv := buildServer(t, ring, clk, ns) // allowlist: {accesscore}

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	// Signed by auditcore — authenticates fine but not in the {accesscore} allowlist.
	// Inline literal caller-cell satisfies the SVCTOKEN-CALLER-CELL-REQUIRED-01 B-arm.
	req := unsignedReq(t, http.MethodGet, "http://ignored/internal/v1/x")
	if err := auth.SignInternalRequest(context.Background(), ring, "auditcore", req, tid, clk); err != nil {
		t.Fatalf("SignInternalRequest: %v", err)
	}

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v (want nil error + 403 response)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", resp.StatusCode)
	}
}

// T9: resolver miss (cellID not in map) → KindInternal from StaticResolver.
func TestRemoteIntegration_T9_ResolverMiss_KindInternal(t *testing.T) {
	t.Parallel()

	// Resolver with no entry for "configcore".
	resolver := transport.NewStaticResolver(map[string]string{"othercell": "127.0.0.1:9999"})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, &http.Client{}, nil, nil)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/x", nil)
	_, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err == nil {
		t.Fatal("expected error for resolver miss, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInternal {
		t.Errorf("Kind = %v, want KindInternal (wiring error)", ec.Kind)
	}
}

// T8: valid happy-path response body is properly consumable.
func TestRemoteIntegration_T8_ResponseBodyReadable(t *testing.T) {
	t.Parallel()

	type respBody struct {
		Data struct{ Ok bool } `json:"data"`
	}

	ring := mustRing(t)
	ns := mustNonceStore(t)
	clk := clock.Real()
	tid := mustTenantID(t)
	srv := buildServer(t, ring, clk, ns)

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	req := signedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x", ring, tid, clk)

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer resp.Body.Close()

	var body respBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.Data.Ok {
		t.Error("response body data.ok = false, want true")
	}
}
