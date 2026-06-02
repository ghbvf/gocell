package cellmw_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/runtime/http/cellmw"
)

// --- stub mux implementations ---

// stubRouteHandler is a minimal kcell.RouteHandler — only Handle.
type stubRouteHandler struct {
	patterns []string
	handlers []http.Handler
}

func (s *stubRouteHandler) Handle(pattern string, h http.Handler) {
	s.patterns = append(s.patterns, pattern)
	s.handlers = append(s.handlers, h)
}

// fullStubMux additionally implements Prefixer, AuthRouteDeclarer, HTTPContractDeclarer.
type fullStubMux struct {
	stubRouteHandler
	prefix        string
	authMetas     []kcell.AuthRouteMeta
	contractSpecs []contractspec.ContractSpec
}

func (f *fullStubMux) Prefix() string { return f.prefix }

func (f *fullStubMux) DeclareAuthMeta(meta kcell.AuthRouteMeta) error {
	f.authMetas = append(f.authMetas, meta)
	return nil
}

func (f *fullStubMux) DeclareHTTPContract(spec contractspec.ContractSpec) error {
	f.contractSpecs = append(f.contractSpecs, spec)
	return nil
}

// --- tests ---

// TestHeaderInjectMux_Handle verifies that the wrap function is applied to
// every handler registered via Handle.
func TestHeaderInjectMux_Handle(t *testing.T) {
	t.Parallel()
	inner := &stubRouteHandler{}
	const injected = "tenant-abc"
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(
				r.Context(),
			)
			// write injected value as a response header so the test can observe it
			w.Header().Set("X-Injected", injected)
			next.ServeHTTP(w, r)
		})
	}

	mux := cellmw.NewHeaderInjectMux(inner, wrap)
	called := false
	mux.Handle("/test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	if len(inner.patterns) != 1 || inner.patterns[0] != "/test" {
		t.Fatalf("expected pattern /test to be registered on inner, got %v", inner.patterns)
	}

	// Execute the registered handler — it should be the wrapped one.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	inner.handlers[0].ServeHTTP(rec, req)

	if rec.Header().Get("X-Injected") != injected {
		t.Errorf("expected X-Injected=%q from wrap, got %q", injected, rec.Header().Get("X-Injected"))
	}
	if !called {
		t.Error("expected inner handler to be called by wrap")
	}
}

// TestHeaderInjectMux_Prefix_forwarded verifies Prefix() is forwarded when inner
// implements kcell.Prefixer.
func TestHeaderInjectMux_Prefix_forwarded(t *testing.T) {
	t.Parallel()
	inner := &fullStubMux{prefix: "/api/v1/auth"}
	mux := cellmw.NewHeaderInjectMux(inner, noopWrap)

	if got := mux.Prefix(); got != "/api/v1/auth" {
		t.Errorf("Prefix() = %q, want %q", got, "/api/v1/auth")
	}
}

// TestHeaderInjectMux_Prefix_fallback verifies Prefix() returns "" when inner
// does not implement kcell.Prefixer.
func TestHeaderInjectMux_Prefix_fallback(t *testing.T) {
	t.Parallel()
	inner := &stubRouteHandler{} // does not implement Prefixer
	mux := cellmw.NewHeaderInjectMux(inner, noopWrap)

	if got := mux.Prefix(); got != "" {
		t.Errorf("Prefix() = %q, want %q", got, "")
	}
}

// TestHeaderInjectMux_DeclareAuthMeta_forwarded verifies DeclareAuthMeta is
// forwarded to the inner mux when it implements kcell.AuthRouteDeclarer.
func TestHeaderInjectMux_DeclareAuthMeta_forwarded(t *testing.T) {
	t.Parallel()
	inner := &fullStubMux{}
	mux := cellmw.NewHeaderInjectMux(inner, noopWrap)

	meta := kcell.AuthRouteMeta{Method: "POST", Path: "/api/v1/auth/login", Public: true}
	if err := mux.DeclareAuthMeta(meta); err != nil {
		t.Fatalf("DeclareAuthMeta: %v", err)
	}
	if len(inner.authMetas) != 1 || inner.authMetas[0] != meta {
		t.Errorf("expected meta forwarded to inner, got %v", inner.authMetas)
	}
}

// TestHeaderInjectMux_DeclareAuthMeta_noop verifies DeclareAuthMeta returns nil
// when inner does not implement kcell.AuthRouteDeclarer.
func TestHeaderInjectMux_DeclareAuthMeta_noop(t *testing.T) {
	t.Parallel()
	inner := &stubRouteHandler{}
	mux := cellmw.NewHeaderInjectMux(inner, noopWrap)

	if err := mux.DeclareAuthMeta(kcell.AuthRouteMeta{Method: "GET", Path: "/ping"}); err != nil {
		t.Errorf("expected nil error on non-declarer inner, got %v", err)
	}
}

// TestHeaderInjectMux_DeclareHTTPContract_forwarded verifies DeclareHTTPContract
// is forwarded to the inner mux when it implements kcell.HTTPContractDeclarer.
func TestHeaderInjectMux_DeclareHTTPContract_forwarded(t *testing.T) {
	t.Parallel()
	inner := &fullStubMux{}
	mux := cellmw.NewHeaderInjectMux(inner, noopWrap)

	spec := contractspec.ContractSpec{ID: "http.auth.login.v1", Kind: "http"}
	if err := mux.DeclareHTTPContract(spec); err != nil {
		t.Fatalf("DeclareHTTPContract: %v", err)
	}
	if len(inner.contractSpecs) != 1 || inner.contractSpecs[0].ID != spec.ID {
		t.Errorf("expected spec forwarded to inner, got %v", inner.contractSpecs)
	}
}

// TestHeaderInjectMux_DeclareHTTPContract_noop verifies DeclareHTTPContract
// returns nil when inner does not implement kcell.HTTPContractDeclarer.
func TestHeaderInjectMux_DeclareHTTPContract_noop(t *testing.T) {
	t.Parallel()
	inner := &stubRouteHandler{}
	mux := cellmw.NewHeaderInjectMux(inner, noopWrap)

	spec := contractspec.ContractSpec{ID: "http.auth.login.v1", Kind: "http"}
	if err := mux.DeclareHTTPContract(spec); err != nil {
		t.Errorf("expected nil error on non-declarer inner, got %v", err)
	}
}

// TestHeaderInjectMux_InterfaceSatisfaction is a compile-time check that
// *HeaderInjectMux satisfies RouteHandler + Prefixer + AuthRouteDeclarer +
// HTTPContractDeclarer.
var (
	_ kcell.RouteHandler         = (*cellmw.HeaderInjectMux)(nil)
	_ kcell.Prefixer             = (*cellmw.HeaderInjectMux)(nil)
	_ kcell.AuthRouteDeclarer    = (*cellmw.HeaderInjectMux)(nil)
	_ kcell.HTTPContractDeclarer = (*cellmw.HeaderInjectMux)(nil)
)

// noopWrap is a trivial middleware that passes through unchanged.
func noopWrap(next http.Handler) http.Handler { return next }
