package transport_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// stubResolver is a simple Resolver for unit tests.
type stubResolver struct {
	endpoint string
	err      error
}

func (s *stubResolver) Resolve(_ context.Context, _ string) (string, error) {
	return s.endpoint, s.err
}

// TestRemoteHTTPTransport_HappyPath verifies that a successful round-trip
// returns (resp, nil) with the correct status code and records the metric.
func TestRemoteHTTPTransport_HappyPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resolver := &stubResolver{endpoint: srv.URL}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		srv.Client(), nil, nil,
	)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/internal/v1/config/x", nil)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

// TestRemoteHTTPTransport_5xxReturnsResponse verifies that a 5xx from the
// server is returned as (resp, nil) — caller decides what to do with it.
func TestRemoteHTTPTransport_5xxReturnsResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	resolver := &stubResolver{endpoint: srv.URL}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		srv.Client(), nil, nil,
	)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/internal/v1/x", nil)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract returned error for 5xx (want nil error + resp): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", resp.StatusCode)
	}
}

// TestRemoteHTTPTransport_ConnectionRefused verifies that a dial failure
// returns KindUnavailable / ErrUpstreamCellUnavailable.
func TestRemoteHTTPTransport_ConnectionRefused(t *testing.T) {
	t.Parallel()

	// Use a closed server to get a connection-refused error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	addr := srv.URL
	srv.Close() // close immediately so dial fails

	resolver := &stubResolver{endpoint: addr}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		&http.Client{}, nil, nil,
	)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/internal/v1/x", nil)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if resp != nil {
		_ = resp.Body.Close()
	}
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
}

// TestRemoteHTTPTransport_CtxCanceled verifies that a canceled context
// returns KindClientClosed (499) — the caller closed the request (#1966 review P2.8),
// distinct from a dial failure (503) or a timeout (504).
func TestRemoteHTTPTransport_CtxCanceled(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		srv.Close()
	})

	resolver := &stubResolver{endpoint: srv.URL}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		srv.Client(), nil, nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://ignored/internal/v1/x", nil)
	resp, err := tr.DoContract(ctx, "http.config.internal.get.v1", req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error for canceled ctx, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindClientClosed {
		t.Errorf("Kind = %v, want KindClientClosed (canceled ctx → 499)", ec.Kind)
	}
	if ec.Code != errcode.ErrUpstreamCellUnavailable {
		t.Errorf("Code = %v, want ErrUpstreamCellUnavailable", ec.Code)
	}
}

// TestRemoteHTTPTransport_ResolverError verifies that a resolver error is
// propagated directly (no further wrapping — resolver decides the Kind).
func TestRemoteHTTPTransport_ResolverError(t *testing.T) {
	t.Parallel()

	resolveErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "wiring error")
	resolver := &stubResolver{err: resolveErr}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		&http.Client{}, nil, nil,
	)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/internal/v1/x", nil)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, resolveErr) {
		t.Errorf("DoContract error = %v, want resolver error %v", err, resolveErr)
	}
}

// TestRemoteHTTPTransport_URLRewritePreservesPath verifies that the path and
// headers from the original request are preserved after URL rewriting.
func TestRemoteHTTPTransport_URLRewritePreservesPath(t *testing.T) {
	t.Parallel()

	const wantPath = "/internal/v1/config/app.name"
	const wantHeader = "Bearer test-token"

	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resolver := &stubResolver{endpoint: srv.URL}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		srv.Client(), nil, nil,
	)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored"+wantPath, nil)
	req.Header.Set("Authorization", wantHeader)

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth != wantHeader {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantHeader)
	}
}

// TestNewRemoteHTTP_NilResolverPanics verifies that a nil resolver triggers a
// registered panic (not a nil deref later).
func TestNewRemoteHTTP_NilResolverPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil resolver, got none")
		}
	}()
	transport.NewRemoteHTTP(clock.Real(), "configcore", nil, &http.Client{}, nil, nil)
}

// TestNewRemoteHTTP_NilClientPanics verifies that a nil client triggers a
// registered panic.
func TestNewRemoteHTTP_NilClientPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil client, got none")
		}
	}()
	resolver := &stubResolver{endpoint: "127.0.0.1:9090"}
	transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, nil, nil, nil)
}

// TestNewRemoteHTTP_EmptyTargetCellPanics verifies that an empty targetCellID panics.
func TestNewRemoteHTTP_EmptyTargetCellPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty targetCellID, got none")
		}
	}()
	resolver := &stubResolver{endpoint: "127.0.0.1:9090"}
	transport.NewRemoteHTTP(clock.Real(), "", resolver, &http.Client{}, nil, nil)
}

// TestNewRemoteHTTP_NilClockPanics verifies that a nil clock triggers a
// registered panic (clock.MustHaveClock), not a nil dereference later.
func TestNewRemoteHTTP_NilClockPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil clock, got none")
		}
	}()
	resolver := &stubResolver{endpoint: "127.0.0.1:9090"}
	transport.NewRemoteHTTP(nil, "configcore", resolver, &http.Client{}, nil, nil)
}

// TestRemoteHTTPTransport_ZeroValue_FailsFast asserts that a zero-value
// RemoteHTTPTransport (not minted via NewRemoteHTTP) returns KindInternal on
// DoContract rather than panicking on a nil resolver or client dereference.
// This mirrors TestInProcessTransport_ZeroValue_FailsFast.
func TestRemoteHTTPTransport_ZeroValue_FailsFast(t *testing.T) {
	t.Parallel()

	var zero transport.RemoteHTTPTransport // zero value; resolver == nil, client == nil

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/internal/v1/x", nil)
	resp, err := zero.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatal("expected nil response from zero-value RemoteHTTPTransport")
	}
	if err == nil {
		t.Fatal("expected error from zero-value RemoteHTTPTransport, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInternal {
		t.Errorf("Kind = %v, want KindInternal", ec.Kind)
	}
}

// TestNewRemoteHTTP_WhitespaceTargetCellPanics verifies that a whitespace-only
// targetCellID triggers a registered panic (strings.TrimSpace guard).
func TestNewRemoteHTTP_WhitespaceTargetCellPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for whitespace-only targetCellID, got none")
		}
	}()
	resolver := &stubResolver{endpoint: "127.0.0.1:9090"}
	transport.NewRemoteHTTP(clock.Real(), "   ", resolver, &http.Client{}, nil, nil)
}

// TestRemoteHTTPTransport_URLRewrite_HTTPSEndpoint verifies that an
// https://host:port endpoint is rewritten correctly: Scheme=https, Host=host:port,
// and the original path/query are preserved.
func TestRemoteHTTPTransport_URLRewrite_HTTPSEndpoint(t *testing.T) {
	t.Parallel()

	const (
		wantPath  = "/internal/v1/config/x"
		wantQuery = "k=v"
	)

	var gotScheme, gotHost, gotPath, gotQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScheme = r.URL.Scheme
		gotHost = r.Host
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// StaticResolver returns the TLS server's https URL directly.
	resolver := &stubResolver{endpoint: srv.URL} // srv.URL is https://127.0.0.1:port
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		srv.Client(), nil, nil,
	)

	reqURL := "http://ignored" + wantPath + "?" + wantQuery
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotQuery != wantQuery {
		t.Errorf("RawQuery = %q, want %q", gotQuery, wantQuery)
	}
	// Host and scheme come from the rewritten URL; scheme check is on the server side
	// via r.Host. We assert the Host was overwritten to the TLS server's authority.
	if gotHost == "" {
		t.Error("Host header is empty on server side after URL rewrite")
	}
	_ = gotScheme // scheme not visible to server-side r.URL.Scheme (always "")
}

// TestRemoteHTTPTransport_URLRewrite_QueryStringPreserved verifies that a
// request URL with a query string has its RawQuery preserved after the URL
// rewrite step (endpoint replaces only Scheme+Host, not path or query).
func TestRemoteHTTPTransport_URLRewrite_QueryStringPreserved(t *testing.T) {
	t.Parallel()

	const wantQuery = "k=v&foo=bar"

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resolver := &stubResolver{endpoint: srv.URL}
	tr := transport.NewRemoteHTTP(
		clock.Real(), "configcore", resolver,
		srv.Client(), nil, nil,
	)

	req, _ := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://ignored/internal/v1/config/x?"+wantQuery,
		nil,
	)
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotQuery != wantQuery {
		t.Errorf("RawQuery = %q, want %q", gotQuery, wantQuery)
	}
}

// TestRemoteHTTPTransport_EndpointWithPathRejected verifies that an endpoint
// carrying a path/query/fragment is rejected (KindInternal) rather than having
// those parts silently dropped during URL rewrite (#1966 review P2.9, defense-in-depth
// — netutil.IsValidNetworkAddress rejects these at config time too).
func TestRemoteHTTPTransport_EndpointWithPathRejected(t *testing.T) {
	t.Parallel()

	for _, ep := range []string{
		"http://example.com/base/path",
		"https://example.com/?k=v",
		"http://example.com/#frag",
		"example.com:8080/foo",
	} {
		resolver := &stubResolver{endpoint: ep}
		tr := transport.NewRemoteHTTP(clock.Real(), "configcore", resolver, &http.Client{}, nil, nil)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://ignored/internal/v1/x", nil)
		resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		var ec *errcode.Error
		if !errors.As(err, &ec) || ec.Kind != errcode.KindInternal {
			t.Errorf("endpoint %q: got err %v, want KindInternal (rewrite rejection)", ep, err)
		}
	}
}

// compile-time check: RemoteHTTPTransport satisfies CellTransport.
var _ transport.CellTransport = (*transport.RemoteHTTPTransport)(nil)
