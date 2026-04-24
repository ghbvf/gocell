package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Spy tracer for auth tests ──────────────────────────────────────────────

type authSpySpan struct {
	mu    sync.Mutex
	attrs []wrapper.Attr
	ended bool
}

func (s *authSpySpan) SetAttributes(attrs ...wrapper.Attr) {
	s.mu.Lock()
	s.attrs = append(s.attrs, attrs...)
	s.mu.Unlock()
}
func (s *authSpySpan) RecordError(_ error)                      {}
func (s *authSpySpan) SetStatus(_ wrapper.StatusCode, _ string) {}
func (s *authSpySpan) End() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
}
func (s *authSpySpan) attrMap() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.attrs))
	for _, a := range s.attrs {
		out[a.Key] = a.Value
	}
	return out
}

type authSpyTracer struct {
	mu    sync.Mutex
	spans []*authSpySpan
}

func (t *authSpyTracer) Start(ctx context.Context, _ string, _ ...wrapper.Attr) (context.Context, wrapper.Span) {
	s := &authSpySpan{}
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *authSpyTracer) only(tb testing.TB) *authSpySpan {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) != 1 {
		tb.Fatalf("expected 1 span, got %d", len(t.spans))
	}
	return t.spans[0]
}

// setAuthSpy installs tr as package-level tracer and resets to NoopTracer after test.
func setAuthSpy(t *testing.T, tr *authSpyTracer) {
	t.Helper()
	wrapper.SetTracer(tr)
	t.Cleanup(func() { wrapper.SetTracer(wrapper.NoopTracer{}) })
}

// TestMain installs a NoopTracer so all route_test.go tests that call Mount
// with a Contract (triggering wrapper.HTTPHandler) don't panic.
func TestMain(m *testing.M) {
	wrapper.SetTracer(wrapper.NoopTracer{})
	m.Run()
}

func loginContractSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/api/v1/auth/login",
	}
}

func TestMount_ContractDrivenRoute_RegistersAtRouteMethodPath(t *testing.T) {
	mux := newCaptureMux()
	var handlerCalled bool
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Method:   "POST",
		Path:     "/api/v1/auth/login",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	})

	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler not invoked")
	require.Len(t, mux.metas, 1)
	assert.Equal(t, "POST", mux.metas[0].Method)
	assert.Equal(t, "/api/v1/auth/login", mux.metas[0].Path)
	assert.True(t, mux.metas[0].Public)
}

func TestMount_ContractDrivenRoute_WrapsHandlerForContractID(t *testing.T) {
	mux := newCaptureMux()
	var seenContract string
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Method:   "POST",
		Path:     "/api/v1/auth/login",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenContract = wrapper.ContractIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	})

	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "http.auth.login.v1", seenContract,
		"wrapper.HTTPHandler must have installed ContractID into context")
}

func TestMount_PanicsWhenContractMethodMismatchesRoute(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
		assert.Contains(t, r.(string), "does not match Contract.Method")
	}()
	Mount(newCaptureMux(), Route{
		Contract: loginContractSpec(), // Contract.Method = "POST"
		Method:   "GET",               // deliberate mismatch
		Path:     "/api/v1/auth/login",
		Handler:  noopHandler,
	})
}

func TestMount_PanicsOnNonHTTPContractKind(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
		assert.Contains(t, r.(string), "Contract.Kind")
	}()
	Mount(newCaptureMux(), Route{
		Contract: wrapper.ContractSpec{ID: "event.x.v1", Kind: "event", Transport: "amqp", Topic: "x"},
		Method:   "POST",
		Path:     "/x",
		Handler:  noopHandler,
	})
}

func TestMount_LegacyFields_NoContract_RoutesWithoutWrapper(t *testing.T) {
	mux := newCaptureMux()
	var seenContract string
	Mount(mux, Route{
		Method: "GET",
		Path:   "/legacy",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Legacy registrations don't flow through wrapper, so no
			// ContractID is injected — the absence is the test.
			if v, ok := ctxkeys.ContractIDFrom(r.Context()); ok {
				seenContract = v
			}
			w.WriteHeader(http.StatusOK)
		}),
	})

	req := httptest.NewRequest("GET", "/legacy", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "", seenContract, "legacy route must not carry ContractID in ctx")
}

func TestMount_HandlerNil_Panics(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
	}()
	// Use matching Route.Path so validateContractShape passes before Handler nil check.
	Mount(newCaptureMux(), Route{Contract: loginContractSpec(), Method: "POST", Path: "/api/v1/auth/login"})
}

func TestMount_PathNormalised(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{in: "/a//b/../c", want: "/a/c"},
		{in: "/login/", want: "/login"}, // path.Clean strips trailing slashes
		{in: "/api/v1/access/login", want: "/api/v1/access/login"},
		{in: "/a//b", want: "/a/b"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			mux := newCaptureMux()
			Mount(mux, Route{Method: "GET", Path: tc.in, Handler: noopHandler})
			require.Len(t, mux.metas, 1)
			assert.Equal(t, tc.want, mux.metas[0].Path)
		})
	}
}

// TestMount_PolicyDenyEmitsContractSpan verifies A3: with RequirePolicy inside
// wrapper.HTTPHandler, a denied request (403) still produces a contract span
// annotated with gocell.contract.id.
func TestMount_PolicyDenyEmitsContractSpan(t *testing.T) {
	tr := &authSpyTracer{}
	setAuthSpy(t, tr)

	mux := newCaptureMux()
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Method:   "POST",
		Path:     "/api/v1/auth/login",
		Handler:  noopHandler,
		Policy:   func(_ *http.Request) error { return fmt.Errorf("test: access denied") },
	})

	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.GreaterOrEqual(t, rec.Code, http.StatusBadRequest, "deny policy must return 4xx or 5xx")

	span := tr.only(t)
	assert.True(t, span.ended, "span must be ended even on policy deny")
	attrs := span.attrMap()
	assert.Equal(t, "http.auth.login.v1", attrs["gocell.contract.id"],
		"gocell.contract.id must be set on deny span")
}

// TestMount_ContractPathMustSuffix tests A4-a: Contract.Path must be a suffix
// of (cleaned) Route.Path when a Contract is provided.
func TestMount_ContractPathMustSuffix(t *testing.T) {
	cases := []struct {
		name          string
		route         Route
		wantPanic     bool
		panicContains string
	}{
		{
			name: "matching suffix passes",
			route: Route{
				Contract: wrapper.ContractSpec{
					ID: "http.users.get.v1", Kind: "http", Transport: "http",
					Method: "GET", Path: "/api/v1/users/{id}",
				},
				Method:  "GET",
				Path:    "/api/v1/users/{id}",
				Handler: noopHandler,
			},
			wantPanic: false,
		},
		{
			name: "sub-path suffix passes",
			route: Route{
				Contract: wrapper.ContractSpec{
					ID: "http.users.get.v1", Kind: "http", Transport: "http",
					Method: "GET", Path: "/api/v1/users/{id}",
				},
				Method:  "GET",
				Path:    "/{id}", // chi sub-route: prefix /api/v1/users stripped
				Handler: noopHandler,
			},
			wantPanic: false,
		},
		{
			name: "path mismatch panics",
			route: Route{
				Contract: wrapper.ContractSpec{
					ID: "http.users.get.v1", Kind: "http", Transport: "http",
					Method: "GET", Path: "/api/v1/users/{id}",
				},
				Method:  "GET",
				Path:    "/api/v1/orders/{id}", // wrong
				Handler: noopHandler,
			},
			wantPanic:     true,
			panicContains: "not a suffix",
		},
		{
			name: "empty Contract.Path panics when ID set",
			route: Route{
				Contract: wrapper.ContractSpec{
					ID:        "http.users.get.v1",
					Kind:      "http",
					Transport: "http",
					Method:    "GET",
					// Path intentionally empty
				},
				Method:  "GET",
				Path:    "/api/v1/users/{id}",
				Handler: noopHandler,
			},
			wantPanic:     true,
			panicContains: "Contract.Path must not be empty",
		},
		{
			name: "no Contract skips path check",
			route: Route{
				Method:  "GET",
				Path:    "/any-path",
				Handler: noopHandler,
			},
			wantPanic: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantPanic {
				defer func() {
					r := recover()
					require.NotNil(t, r, "expected panic")
					assert.Contains(t, r.(string), tc.panicContains)
				}()
			}
			Mount(newCaptureMux(), tc.route)
		})
	}
}
