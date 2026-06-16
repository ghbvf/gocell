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

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// ---- helpers ----------------------------------------------------------------

// testTenantID is a canonical UUID used across integration tests.
const testTenantIDStr = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

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
//
// The server handler blocks on r.Context().Done() so the timeout fires while
// waiting for the response (not in a racy dial window). A controllable cancel
// is used to ensure the deadline fires only after the connection is established
// and the server is blocking — making the test deterministic rather than relying
// on a 1 ms wall-clock race.
func TestRemoteIntegration_T4_CtxDeadline(t *testing.T) {
	t.Parallel()

	// reqReceived signals that the server handler has been entered — the
	// connection is established and the request is in-flight — so the test can
	// cancel deterministically without a wall-clock sleep / timing race.
	reqReceived := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reqReceived)
		<-r.Context().Done() // block until the client cancels.
	}))
	t.Cleanup(srv.Close)

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://ignored/x", nil)
		resp, err := tr.DoContract(ctx, "http.config.internal.get.v1", req)
		ch <- result{resp, err}
	}()

	<-reqReceived // deterministic: handler reached, request in-flight.
	cancel()

	res := <-ch
	if res.resp != nil {
		_ = res.resp.Body.Close()
	}
	if res.err == nil {
		t.Fatal("expected error for canceled ctx, got nil")
	}
	var ec *errcode.Error
	if !errors.As(res.err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", res.err, res.err)
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

// T10: nonce replay (FR-005) — a replayed service token is rejected with 401.
//
// The same signed Authorization header value is sent twice to a server that
// has a NonceStore. The first request must succeed (200) and the second must
// fail (401) because the nonce has already been consumed.
func TestRemoteIntegration_T10_NonceReplay_Rejected(t *testing.T) {
	t.Parallel()

	ring := mustRing(t)
	ns := mustNonceStore(t)
	clk := clock.Real()
	tid := mustTenantID(t)
	srv := buildServer(t, ring, clk, ns)

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	// Build two independent requests that carry the same Authorization header
	// (same nonce embedded in the token) to simulate a replay.
	// We sign the first request and extract the Authorization header value,
	// then stamp it onto a second, fresh request.
	req1 := signedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x", ring, tid, clk)
	authHeader := req1.Header.Get("Authorization")
	if authHeader == "" {
		t.Fatal("Authorization header must be set after signing")
	}

	// First dispatch: should succeed (200).
	resp1, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req1)
	if err != nil {
		t.Fatalf("T10 first DoContract: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("T10 first request: StatusCode = %d, want 200", resp1.StatusCode)
	}

	// Second dispatch: replay the same token (nonce already consumed → 401).
	req2 := unsignedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x")
	req2.Header.Set("Authorization", authHeader)
	req2.Header.Set(auth.HeaderTenantID, tid.String())

	resp2, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req2)
	if err != nil {
		t.Fatalf("T10 replay DoContract: %v (want nil error + 401 response)", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("T10 replay: StatusCode = %d, want 401 (nonce replay must be rejected)", resp2.StatusCode)
	}
}

// T11: X-Gocell-Principal end-to-end — when DoContract carries a principal in
// ctx, SignInternalRequest embeds it in X-Gocell-Principal; the server's
// ServiceTokenMiddleware rebuilds actor/subject/session into the request context,
// making them visible to the business handler.
func TestRemoteIntegration_T11_PrincipalPropagation_EndToEnd(t *testing.T) {
	t.Parallel()

	ring := mustRing(t)
	clk := clock.Real()
	tid := mustTenantID(t)
	ns := mustNonceStore(t)

	const (
		wantActor   = "usr-propagated-actor"
		wantSubject = "usr-propagated-subject"
		wantSession = "sess-propagated-123"
	)

	// Server that captures the principal fields rebuilt by ServiceTokenMiddleware.
	var (
		capturedActor   string
		capturedSubject string
		capturedSession string
		mu              sync.Mutex
	)

	biz := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, _ := ctxkeys.ActorIDFrom(r.Context())
		subject, _ := ctxkeys.SubjectIDFrom(r.Context())
		session, _ := ctxkeys.SessionIDFrom(r.Context())
		mu.Lock()
		capturedActor = actor
		capturedSubject = subject
		capturedSession = session
		mu.Unlock()
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

	resolver := transport.NewStaticResolver(map[string]string{"configcore": srv.URL})
	tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, srv.Client(), nil, nil)

	// Build a ctx with business principal fields (as injectPrincipalCtxKeys would
	// set them after JWT auth).
	ctx := ctxkeys.WithActorID(context.Background(), wantActor)
	ctx = ctxkeys.WithSubjectID(ctx, wantSubject)
	ctx = ctxkeys.WithSessionID(ctx, wantSession)

	// Sign: SignInternalRequest encodes ctx principal into X-Gocell-Principal.
	req := unsignedReq(t, http.MethodGet, "http://ignored/internal/v1/config/x")
	req = req.WithContext(ctx)
	if err := auth.SignInternalRequest(ctx, ring, "accesscore", req, tid, clk); err != nil {
		t.Fatalf("SignInternalRequest: %v", err)
	}

	// Check X-Gocell-Principal header was set before sending.
	if req.Header.Get(auth.HeaderPrincipal) == "" {
		t.Error("X-Gocell-Principal must be set when ctx has a principal")
	}

	resp, err := tr.DoContract(ctx, "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("T11 DoContract: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("T11 status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	gotActor := capturedActor
	gotSubject := capturedSubject
	gotSession := capturedSession
	mu.Unlock()

	if gotActor != wantActor {
		t.Errorf("T11 server actor = %q, want %q", gotActor, wantActor)
	}
	if gotSubject != wantSubject {
		t.Errorf("T11 server subject = %q, want %q", gotSubject, wantSubject)
	}
	if gotSession != wantSession {
		t.Errorf("T11 server session = %q, want %q", gotSession, wantSession)
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
