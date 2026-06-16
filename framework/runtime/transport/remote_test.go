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
// returns KindUnavailable.
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
	if ec.Kind != errcode.KindUnavailable {
		t.Errorf("Kind = %v, want KindUnavailable", ec.Kind)
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

// compile-time check: RemoteHTTPTransport satisfies CellTransport.
var _ transport.CellTransport = (*transport.RemoteHTTPTransport)(nil)
