package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// noopHandler is the minimal non-nil handler used by validation tests.
var noopHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

// captureMux pairs a stdlib ServeMux with an AuthRouteDeclarer counter so
// tests can assert both sides of auth.Declare's dispatch in a single mux.
type captureMux struct {
	*http.ServeMux
	metas []cell.AuthRouteMeta
}

func newCaptureMux() *captureMux {
	return &captureMux{ServeMux: http.NewServeMux()}
}

func (m *captureMux) Handle(pattern string, handler http.Handler) {
	m.ServeMux.Handle(pattern, handler)
}

func (m *captureMux) DeclareAuthMeta(meta cell.AuthRouteMeta) {
	m.metas = append(m.metas, meta)
}

var (
	_ cell.RouteHandler      = (*captureMux)(nil)
	_ cell.AuthRouteDeclarer = (*captureMux)(nil)
)

// ---------------------------------------------------------------------------
// RouteDecl.validateOrPanic
// ---------------------------------------------------------------------------

func TestRouteDecl_Validate_Panics(t *testing.T) {
	cases := []struct {
		name    string
		decl    RouteDecl
		wantMsg string
	}{
		{
			name:    "empty Method",
			decl:    RouteDecl{Path: "/x", Handler: noopHandler},
			wantMsg: "auth.Mount: Method must not be empty",
		},
		{
			name:    "unknown Method",
			decl:    RouteDecl{Method: "FOO", Path: "/x", Handler: noopHandler},
			wantMsg: "auth.Mount: method \"FOO\" not recognised",
		},
		{
			name:    "lowercase Method",
			decl:    RouteDecl{Method: "post", Path: "/x", Handler: noopHandler},
			wantMsg: "auth.Mount: Method \"post\" must be upper-case",
		},
		{
			name:    "relative Path",
			decl:    RouteDecl{Method: "GET", Path: "relative", Handler: noopHandler},
			wantMsg: "auth.Mount: Path \"relative\" must start with '/'",
		},
		{
			name:    "empty Path",
			decl:    RouteDecl{Method: "GET", Path: "", Handler: noopHandler},
			wantMsg: "auth.Mount: Path \"\" must start with '/'",
		},
		{
			name:    "nil Handler",
			decl:    RouteDecl{Method: "GET", Path: "/x"},
			wantMsg: "auth.Mount: Handler must not be nil",
		},
		{
			name: "Public with Policy",
			decl: RouteDecl{
				Method:  "POST",
				Path:    "/login",
				Handler: noopHandler,
				Public:  true,
				Policy:  Authenticated(),
			},
			wantMsg: "auth.Mount POST /login: Public=true conflicts with non-nil Policy",
		},
		{
			name: "Public with PasswordResetExempt",
			decl: RouteDecl{
				Method:              "GET",
				Path:                "/x",
				Handler:             noopHandler,
				Public:              true,
				PasswordResetExempt: true,
			},
			wantMsg: "auth.Mount GET /x: Public=true conflicts with PasswordResetExempt=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				require.NotNil(t, r, "expected panic")
				assert.Contains(t, r.(string), tc.wantMsg)
			}()
			Declare(newCaptureMux(), tc.decl)
		})
	}
}

func TestRouteDecl_Validate_Accepts(t *testing.T) {
	decls := []RouteDecl{
		{Method: "GET", Path: "/health", Handler: noopHandler, Public: true},
		{Method: "POST", Path: "/api/v1/users", Handler: noopHandler, Policy: Authenticated()},
		{Method: "DELETE", Path: "/api/v1/sessions/{id}", Handler: noopHandler, Policy: Authenticated(), PasswordResetExempt: true},
		{Method: "PUT", Path: "/api/v1/x", Handler: noopHandler, Delegated: true},
	}
	for _, d := range decls {
		t.Run(d.Method+" "+d.Path, func(t *testing.T) {
			assert.NotPanics(t, func() { Declare(newCaptureMux(), d) })
		})
	}
}

// ---------------------------------------------------------------------------
// Declare routing + metadata forwarding
// ---------------------------------------------------------------------------

func TestDeclare_StdlibServeMux_RoutesWithoutMeta(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	Declare(mux, RouteDecl{
		Method: "GET",
		Path:   "/api/v1/ping",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called, "handler must be invoked via stdlib ServeMux")
}

func TestDeclare_AuthRouteDeclarer_ForwardsMeta(t *testing.T) {
	mux := newCaptureMux()

	Declare(mux, RouteDecl{
		Method:  "POST",
		Path:    "/api/v1/access/sessions/login",
		Handler: noopHandler,
		Public:  true,
	})
	Declare(mux, RouteDecl{
		Method:              "DELETE",
		Path:                "/api/v1/access/sessions/{id}",
		Handler:             noopHandler,
		Policy:              Authenticated(),
		PasswordResetExempt: true,
	})
	Declare(mux, RouteDecl{
		Method:    "POST",
		Path:      "/internal/v1/x",
		Handler:   noopHandler,
		Delegated: true,
	})

	require.Len(t, mux.metas, 3)

	assert.Equal(t, cell.AuthRouteMeta{
		Method: "POST", Path: "/api/v1/access/sessions/login", Public: true,
	}, mux.metas[0])

	assert.Equal(t, cell.AuthRouteMeta{
		Method: "DELETE", Path: "/api/v1/access/sessions/{id}", PasswordResetExempt: true,
	}, mux.metas[1])

	assert.Equal(t, cell.AuthRouteMeta{
		Method: "POST", Path: "/internal/v1/x", Delegated: true,
	}, mux.metas[2])
}

func TestDeclare_NormalisesPath(t *testing.T) {
	mux := newCaptureMux()
	Declare(mux, RouteDecl{
		Method:  "GET",
		Path:    "/a///b/../b",
		Handler: noopHandler,
		Public:  true,
	})
	require.Len(t, mux.metas, 1)
	assert.Equal(t, "/a/b", mux.metas[0].Path)
}

// ---------------------------------------------------------------------------
// RequirePolicy behaviour
// ---------------------------------------------------------------------------

func TestRequirePolicy_NilPanics(t *testing.T) {
	assert.PanicsWithValue(t, "auth.RequirePolicy: policy must not be nil", func() {
		RequirePolicy(nil)
	})
}

func TestRequirePolicy_Allows_And_Rejects(t *testing.T) {
	permit := Policy(func(*http.Request) error { return nil })
	deny := Policy(func(*http.Request) error {
		return errcode.New(errcode.ErrAuthForbidden, "nope")
	})

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("permit", func(t *testing.T) {
		nextCalled = false
		rec := httptest.NewRecorder()
		RequirePolicy(permit)(next).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
		assert.True(t, nextCalled)
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("deny", func(t *testing.T) {
		nextCalled = false
		rec := httptest.NewRecorder()
		RequirePolicy(deny)(next).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
		assert.False(t, nextCalled, "next must not be called when policy fails")
		assert.Equal(t, http.StatusForbidden, rec.Code)

		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, string(errcode.ErrAuthForbidden), body.Error.Code)
	})
}

// TestRequirePolicy_Parity ensures RequirePolicy preserves the behaviour that
// callers previously relied on from the legacy auth.Secured helper: same
// short-circuit, same error mapping, same context plumbing.
func TestRequirePolicy_Parity(t *testing.T) {
	// A Policy that inspects context and r.PathValue — the two capabilities
	// Secured exposed that simpler middleware libraries lack.
	policy := Policy(func(r *http.Request) error {
		if r.PathValue("id") != "self" {
			return errcode.New(errcode.ErrAuthForbidden, "not self")
		}
		if _, ok := r.Context().Value(ctxSentinelKey{}).(string); !ok {
			return errcode.New(errcode.ErrAuthUnauthorized, "missing ctx")
		}
		return nil
	})

	handlerCalls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusOK)
	})

	mw := RequirePolicy(policy)(next)

	cases := []struct {
		name    string
		id      string
		withCtx bool
		want    int
	}{
		{"ok", "self", true, http.StatusOK},
		{"wrong id", "other", true, http.StatusForbidden},
		{"missing ctx", "self", false, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.Handle("POST /users/{id}", mw)

			req := httptest.NewRequest(http.MethodPost, "/users/"+tc.id, strings.NewReader(""))
			if tc.withCtx {
				req = req.WithContext(context.WithValue(req.Context(), ctxSentinelKey{}, "yes"))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

type ctxSentinelKey struct{}
