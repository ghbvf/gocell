package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// captureMux pairs a stdlib ServeMux with an AuthRouteDeclarer counter so
// tests can assert both sides of auth.Mount's dispatch in a single mux.
type captureMux struct {
	*http.ServeMux
	metas []cell.AuthRouteMeta
}

func newCaptureMux() *captureMux { return &captureMux{ServeMux: http.NewServeMux()} }

func (m *captureMux) Handle(pattern string, h http.Handler) { m.ServeMux.Handle(pattern, h) }

func (m *captureMux) DeclareAuthMeta(meta cell.AuthRouteMeta) error {
	m.metas = append(m.metas, meta)
	return nil
}

var (
	_ cell.RouteHandler      = (*captureMux)(nil)
	_ cell.AuthRouteDeclarer = (*captureMux)(nil)
)

type failingDeclareMux struct {
	*http.ServeMux
	handleCalls int
}

func newFailingDeclareMux() *failingDeclareMux {
	return &failingDeclareMux{ServeMux: http.NewServeMux()}
}

func (m *failingDeclareMux) Handle(pattern string, h http.Handler) {
	m.handleCalls++
	m.ServeMux.Handle(pattern, h)
}

func (m *failingDeclareMux) DeclareAuthMeta(cell.AuthRouteMeta) error {
	return assert.AnError
}

var (
	_ cell.RouteHandler      = (*failingDeclareMux)(nil)
	_ cell.AuthRouteDeclarer = (*failingDeclareMux)(nil)
)

type failingContractDeclareMux struct {
	*http.ServeMux
	handleCalls int
}

func newFailingContractDeclareMux() *failingContractDeclareMux {
	return &failingContractDeclareMux{ServeMux: http.NewServeMux()}
}

func (m *failingContractDeclareMux) Handle(pattern string, h http.Handler) {
	m.handleCalls++
	m.ServeMux.Handle(pattern, h)
}

func (m *failingContractDeclareMux) DeclareHTTPContract(contractspec.ContractSpec) error {
	return assert.AnError
}

var (
	_ cell.RouteHandler         = (*failingContractDeclareMux)(nil)
	_ cell.HTTPContractDeclarer = (*failingContractDeclareMux)(nil)
)

var noopHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

func loginContractSpec() contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID: "http.auth.login.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
		Method: "POST", Path: "/api/v1/access/sessions/login",
	}
}

func TestMount_ContractDrivenRoute_RegistersAndForwardsMeta(t *testing.T) {
	mux := newCaptureMux()
	handlerCalled := false
	require.NoError(t, Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	}))

	req := httptest.NewRequest("POST", "/api/v1/access/sessions/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.True(t, handlerCalled)
	require.Len(t, mux.metas, 1)
	assert.Equal(t, "POST", mux.metas[0].Method)
	assert.Equal(t, "/api/v1/access/sessions/login", mux.metas[0].Path)
	assert.True(t, mux.metas[0].Public)
}

func TestMount_WritesContractIDIntoContext(t *testing.T) {
	mux := newCaptureMux()
	var seen string
	require.NoError(t, Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = ctxkeys.ContractIDFrom(r.Context())
		}),
		Public: true,
	}))
	req := httptest.NewRequest("POST", "/api/v1/access/sessions/login", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "http.auth.login.v1", seen)
}

func TestMount_DeclareAuthMetaErrorDoesNotRegisterRoute(t *testing.T) {
	mux := newFailingDeclareMux()

	err := Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler:  noopHandler,
		Public:   true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "declare auth metadata")
	assert.Zero(t, mux.handleCalls, "Mount must validate and declare metadata before registering the route")
}

func TestMount_DeclareHTTPContractErrorDoesNotRegisterRoute(t *testing.T) {
	mux := newFailingContractDeclareMux()

	err := Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler:  noopHandler,
		Public:   true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "declare HTTP contract metadata")
	assert.Zero(t, mux.handleCalls, "Mount must declare HTTP contract metadata before registering the route")
}

func TestMount_AppliesPolicyBeforeHandler(t *testing.T) {
	mux := newCaptureMux()
	policyCalled := false
	handlerCalled := false

	err := Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusNoContent)
		}),
		Policy: func(_ *http.Request) error {
			policyCalled = true
			return nil
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/api/v1/access/sessions/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.True(t, policyCalled)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMount_ReturnsErrorOnMissingContractID(t *testing.T) {
	err := Mount(newCaptureMux(), Route{Handler: noopHandler})
	require.Error(t, err)
}

func TestMount_ReturnsErrorOnNilHandler(t *testing.T) {
	err := Mount(newCaptureMux(), Route{Contract: loginContractSpec()})
	require.Error(t, err)
}

func TestMount_ReturnsErrorOnNonHTTPKind(t *testing.T) {
	err := Mount(newCaptureMux(), Route{
		Contract: contractspec.ContractSpec{ID: "event.x.v1", Kind: cellvocab.ContractEvent, Transport: "amqp", Topic: "x"},
		Handler:  noopHandler,
	})
	require.Error(t, err)
}

func TestMount_ReturnsErrorOnInvalidMethod(t *testing.T) {
	err := Mount(newCaptureMux(), Route{
		Contract: contractspec.ContractSpec{
			ID: "http.x.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
			Method: "foo", Path: "/x",
		},
		Handler: noopHandler,
	})
	require.Error(t, err)
}

func TestMount_ReturnsErrorOnPublicWithPolicy(t *testing.T) {
	err := Mount(newCaptureMux(), Route{
		Contract: loginContractSpec(),
		Handler:  noopHandler,
		Public:   true,
		Policy:   requireAuthenticatedPolicy(),
	})
	require.Error(t, err)
}

func TestMustMount_PanicsOnNilHandler(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "expected panic on nil Handler")
	}()
	MustMount(newCaptureMux(), Route{Contract: loginContractSpec()})
}

func TestRequirePolicy_NilReturnsError(t *testing.T) {
	middleware, err := RequirePolicy(nil)
	require.Error(t, err)
	assert.Nil(t, middleware)
	assert.Contains(t, err.Error(), "policy must not be nil")
}

// prefixedCaptureMux is a test double that extends captureMux with a fixed
// prefix, implementing cell.Prefixer so auth.Mount exercises the
// Prefixer branch.
type prefixedCaptureMux struct {
	*http.ServeMux
	prefix string
	metas  []cell.AuthRouteMeta
}

func newPrefixedCaptureMux(prefix string) *prefixedCaptureMux {
	return &prefixedCaptureMux{ServeMux: http.NewServeMux(), prefix: prefix}
}

func (m *prefixedCaptureMux) Handle(pattern string, h http.Handler) {
	m.ServeMux.Handle(pattern, h)
}

func (m *prefixedCaptureMux) DeclareAuthMeta(meta cell.AuthRouteMeta) error {
	m.metas = append(m.metas, meta)
	return nil
}

func (m *prefixedCaptureMux) Prefix() string { return m.prefix }

var (
	_ cell.RouteHandler      = (*prefixedCaptureMux)(nil)
	_ cell.AuthRouteDeclarer = (*prefixedCaptureMux)(nil)
	_ cell.Prefixer          = (*prefixedCaptureMux)(nil)
)

func TestStripMountPrefix_PathSegmentBoundary(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		fullPath string
		want     string
	}{
		{
			name:     "empty prefix returns fullPath unchanged",
			prefix:   "",
			fullPath: "/api/v1/x",
			want:     "/api/v1/x",
		},
		{
			name:     "prefix equals fullPath returns /",
			prefix:   "/api/v1/access",
			fullPath: "/api/v1/access",
			want:     "/",
		},
		{
			name:     "prefix equals fullPath with trailing slash returns /",
			prefix:   "/api/v1/access",
			fullPath: "/api/v1/access/",
			want:     "/",
		},
		{
			name:     "valid segment prefix strips correctly",
			prefix:   "/api/v1/access",
			fullPath: "/api/v1/access/sessions",
			want:     "/sessions",
		},
		{
			name:     "partial segment match is not a prefix — bug case /api/v1/a vs /api/v1/auth/x",
			prefix:   "/api/v1/a",
			fullPath: "/api/v1/auth/x",
			want:     "/api/v1/auth/x",
		},
		{
			name:     "prefix is substring but not segment prefix — /api/v1/accessx vs /api/v1/access/x",
			prefix:   "/api/v1/accessx",
			fullPath: "/api/v1/access/x",
			want:     "/api/v1/access/x",
		},
		{
			name:     "prefix not at start of fullPath",
			prefix:   "/access",
			fullPath: "/api/v1/access/x",
			want:     "/api/v1/access/x",
		},
		{
			name:     "short prefix /api/v1 strips correctly",
			prefix:   "/api/v1",
			fullPath: "/api/v1/access",
			want:     "/access",
		},
		{
			name:     "leading double-slash before prefix not a segment match",
			prefix:   "/api/v1/access",
			fullPath: "//api/v1/access/x",
			want:     "//api/v1/access/x",
		},
		{
			name:     "internal v1 prefix does not match public v1 path",
			prefix:   "/internal/v1",
			fullPath: "/api/v1/x",
			want:     "/api/v1/x",
		},
		{
			name:     "public v1 prefix does not match internal v1 path",
			prefix:   "/api/v1",
			fullPath: "/internal/v1/x",
			want:     "/internal/v1/x",
		},
		{
			name:     "deeper segment under same prefix root",
			prefix:   "/api/v1/access",
			fullPath: "/api/v1/access/sessions/login",
			want:     "/sessions/login",
		},
		{
			// stripMountPrefix called directly with prefix "/" — isPathSegmentPrefix
			// returns false (next char is 'a' not '/'), so the early return kicks
			// in and the path is preserved unchanged. Documents the invariant
			// that the helper itself does not need to special-case "/", because
			// Mount normalises "/" → "" before reaching here.
			name:     "root prefix is treated as no-op via isPathSegmentPrefix=false",
			prefix:   "/",
			fullPath: "/api/v1/x",
			want:     "/api/v1/x",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMountPrefix(tc.fullPath, tc.prefix)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMount_AcceptsRootPrefix pins the F2 fix: a mux that reports
// Prefix() == "/" must NOT trigger the prefix-mismatch panic. Root
// prefix is normalised to no prefix, so contract paths are registered
// at their absolute form (chi at root owns the whole tree).
func TestMount_AcceptsRootPrefix(t *testing.T) {
	mux := newPrefixedCaptureMux("/")
	require.NotPanics(t, func() {
		require.NoError(t, Mount(mux, Route{
			Contract: contractspec.ContractSpec{
				ID: "http.auth.login.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
				Method: "POST", Path: "/api/v1/access/sessions/login",
			},
			Handler: noopHandler,
			Public:  true,
		}))
	})
	require.Len(t, mux.metas, 1)
	// Path is registered at its absolute form (no relative-path stripping
	// for root mount).
	assert.Equal(t, "/api/v1/access/sessions/login", mux.metas[0].Path)
}

func TestMount_ReturnsErrorOnPrefixMismatch(t *testing.T) {
	mux := newPrefixedCaptureMux("/api/v1/access")
	err := Mount(mux, Route{
		Contract: contractspec.ContractSpec{
			ID: "http.foo.bar.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
			Method: "GET", Path: "/foo/bar",
		},
		Handler: noopHandler,
		Public:  true,
	})
	require.Error(t, err)
}

func TestMount_ReturnsErrorOnPartialSegmentPrefix(t *testing.T) {
	mux := newPrefixedCaptureMux("/api/v1/a")
	err := Mount(mux, Route{
		Contract: contractspec.ContractSpec{
			ID: "http.auth.x.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
			Method: "GET", Path: "/api/v1/auth/x",
		},
		Handler: noopHandler,
		Public:  true,
	})
	require.Error(t, err)
}

func TestMount_ReturnsErrorOnUnrecognizedMethod(t *testing.T) {
	// "FETCH" is uppercase (passes the ToUpper check) but not in validRouteMethods.
	err := Mount(newCaptureMux(), Route{
		Contract: contractspec.ContractSpec{
			ID: "http.x.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
			Method: "FETCH", Path: "/x",
		},
		Handler: noopHandler,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not recognized")
}

func TestMount_ReturnsErrorOnPublicWithPasswordResetExempt(t *testing.T) {
	err := Mount(newCaptureMux(), Route{
		Contract:            loginContractSpec(),
		Handler:             noopHandler,
		Public:              true,
		PasswordResetExempt: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PasswordResetExempt")
}

func TestMount_ReturnsErrorOnEmptyPath(t *testing.T) {
	err := Mount(newCaptureMux(), Route{
		Contract: contractspec.ContractSpec{
			ID:   "http.x.v1",
			Kind: cellvocab.ContractHTTP, Transport: "http",
			Method: "GET",
			Path:   "",
		},
		Handler: noopHandler,
	})
	require.Error(t, err)
}

func TestIsPathSegmentPrefix_LenEqual(t *testing.T) {
	// fullPath == prefix → true; and when len(fullPath) < len(prefix) → false.
	assert.False(t, isPathSegmentPrefix("/api", "/api/v1"))
}

func TestMount_AcceptsValidSegmentPrefix(t *testing.T) {
	mux := newPrefixedCaptureMux("/api/v1/access")
	require.NotPanics(t, func() {
		require.NoError(t, Mount(mux, Route{
			Contract: contractspec.ContractSpec{
				ID: "http.auth.login.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
				Method: "POST", Path: "/api/v1/access/sessions/login",
			},
			Handler: noopHandler,
			Public:  true,
		}))
	})
	require.Len(t, mux.metas, 1)
	assert.Equal(t, "/sessions/login", mux.metas[0].Path)
}
