package celltest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestTestMux_HandleGroupAndWith(t *testing.T) {
	mux := NewTestMux()
	require.NotNil(t, mux)

	groupCalled := false
	mux.Group(func(group cell.RouteMux) {
		groupCalled = true

		group.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))

		wrapped := group.With(func(next http.Handler) http.Handler { return next })
		require.NotNil(t, wrapped)

		wrapped.Handle("GET /ready", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
	})

	assert.True(t, groupCalled)

	t.Run("group-registered route is reachable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("with-registered route is reachable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ready", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
	})
}

func TestTestMux_RouteAndMountStripPrefix(t *testing.T) {
	mux := NewTestMux()

	mux.Route("/api", func(sub cell.RouteMux) {
		sub.Handle("GET /items/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(r.PathValue("id")))
		}))
	})

	mounted := http.NewServeMux()
	mounted.Handle("GET /status", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	mux.Mount("/internal", mounted)

	t.Run("route strips prefix and preserves path values", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/items/42", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "42", rec.Body.String())
	})

	t.Run("mount strips prefix before handing off to the mounted handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "/status", rec.Body.String())
	})
}

// okHandler is a minimal handler that always responds 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestTestMux_DeclareAuthMeta_RecordsOnRoot verifies that auth.Mount on a
// root TestMux records the declared AuthRouteMeta accessible via DeclaredAuthMetas.
func TestTestMux_DeclareAuthMeta_RecordsOnRoot(t *testing.T) {
	m := NewTestMux()

	auth.Mount(m, auth.Route{Contract: testHTTPContract("POST", "/api/v1/foo"), Handler: okHandler, Policy: auth.AnyRole("admin")})
	auth.Mount(m, auth.Route{Contract: testHTTPContract("GET", "/api/v1/bar"), Handler: okHandler, Public: true})

	metas := m.DeclaredAuthMetas()
	require.Len(t, metas, 2)
	assert.Equal(t, cell.AuthRouteMeta{Method: "POST", Path: "/api/v1/foo"}, metas[0])
	assert.Equal(t, cell.AuthRouteMeta{Method: "GET", Path: "/api/v1/bar", Public: true}, metas[1])
}

// TestTestMux_Route_ComposesPrefix verifies that sub-muxes produced by Route
// forward DeclareAuthMeta to the root with the full composed path.
// Mirrors the nested-prefix regression pattern in
// runtime/http/router/router_authmeta_test.go.
// TestTestMux_Route_CollectionRootDualRegistration ensures that two routes
// on the same sub-mux whose Contract.Paths differ only in trailing slash
// (both collapse to the "/" relative pattern after strip) do not panic
// when registered with distinct HTTP methods. Go 1.22 ServeMux disambiguates
// by method, so "POST /api/v1/config" + "GET /api/v1/config" is legal and
// both entries must be matchable by a request.
func TestTestMux_Route_CollectionRootDualRegistration(t *testing.T) {
	root := NewTestMux()
	root.Route("/api/v1/config", func(sub cell.RouteMux) {
		// Both routes map to the collection-root relative path "/", hit by
		// auth.Mount as "POST /" and "GET /" after stripMountPrefix.
		auth.Mount(sub, auth.Route{Contract: testHTTPContract("POST", "/api/v1/config"), Handler: okHandler, Public: true})
		auth.Mount(sub, auth.Route{Contract: testHTTPContract("GET", "/api/v1/config"), Handler: okHandler, Public: true})
	})

	for _, tc := range []struct {
		method, path string
	}{
		{"POST", "/api/v1/config"},
		{"POST", "/api/v1/config/"}, // trailing slash alias
		{"GET", "/api/v1/config"},
		{"GET", "/api/v1/config/"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		root.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "%s %s must match", tc.method, tc.path)
	}
}

// TestTestMux_Route_DuplicateMethodPatternPanics documents that
// registering the same (method, pattern) pair twice on a shared root mux
// surfaces via the stdlib ServeMux panic. This is the intentional
// fail-fast when a test accidentally mounts a contract twice under the
// same Route.
func TestTestMux_Route_DuplicateMethodPatternPanics(t *testing.T) {
	root := NewTestMux()
	root.Route("/api/v1/config", func(sub cell.RouteMux) {
		auth.Mount(sub, auth.Route{Contract: testHTTPContract("POST", "/api/v1/config"), Handler: okHandler, Public: true})
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on duplicate POST /api/v1/config registration")
			}
		}()
		auth.Mount(sub, auth.Route{Contract: testHTTPContract("POST", "/api/v1/config"), Handler: okHandler, Public: true})
	})
}

func TestTestMux_Route_ComposesPrefix(t *testing.T) {
	root := NewTestMux()

	root.Route("/api/v1", func(v1 cell.RouteMux) {
		v1.Route("/access", func(acc cell.RouteMux) {
			acc.Route("/sessions", func(sess cell.RouteMux) {
				// Contract.Path is fully qualified per production convention;
				// auth.Mount strips the nested mux prefix to derive the
				// chi-relative registration path.
				auth.Mount(sess, auth.Route{Contract: testHTTPContract("POST", "/api/v1/access/sessions/login"), Handler: okHandler, Public: true})
				auth.Mount(sess, auth.Route{Contract: testHTTPContract("DELETE", "/api/v1/access/sessions/{id}"), Handler: okHandler, Policy: func(r *http.Request) error {
					// local helper to avoid kernel/ depending on runtime/auth/authtest
					p, ok := auth.FromContext(r.Context())
					if !ok {
						return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
					}
					if p.Kind == auth.PrincipalAnonymous {
						return errcode.New(errcode.ErrAuthUnauthorized, "anonymous principal not permitted")
					}
					if p.Kind == auth.PrincipalUser && p.Subject == "" {
						return errcode.New(errcode.ErrAuthUnauthorized, "principal subject missing")
					}
					return nil
				}, PasswordResetExempt: true})
			})
		})
	})

	metas := root.DeclaredAuthMetas()
	require.Len(t, metas, 2)
	assert.Equal(t, "/api/v1/access/sessions/login", metas[0].Path)
	assert.True(t, metas[0].Public)
	assert.Equal(t, "/api/v1/access/sessions/{id}", metas[1].Path)
	assert.True(t, metas[1].PasswordResetExempt)
}
