package celltest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
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

// TestTestMux_DeclareAuthMeta_RecordsOnRoot verifies that auth.Declare on a
// root TestMux records the declared AuthRouteMeta accessible via DeclaredAuthMetas.
func TestTestMux_DeclareAuthMeta_RecordsOnRoot(t *testing.T) {
	m := NewTestMux()

	auth.Declare(m, auth.RouteDecl{
		Method:  "POST",
		Path:    "/api/v1/foo",
		Handler: okHandler,
		Policy:  auth.AnyRole("admin"),
	})
	auth.Declare(m, auth.RouteDecl{
		Method:  "GET",
		Path:    "/api/v1/bar",
		Handler: okHandler,
		Public:  true,
	})

	metas := m.DeclaredAuthMetas()
	require.Len(t, metas, 2)
	assert.Equal(t, cell.AuthRouteMeta{Method: "POST", Path: "/api/v1/foo"}, metas[0])
	assert.Equal(t, cell.AuthRouteMeta{Method: "GET", Path: "/api/v1/bar", Public: true}, metas[1])
}

// TestTestMux_Route_ComposesPrefix verifies that sub-muxes produced by Route
// forward DeclareAuthMeta to the root with the full composed path.
// Mirrors the nested-prefix regression pattern in
// runtime/http/router/router_authmeta_test.go.
func TestTestMux_Route_ComposesPrefix(t *testing.T) {
	root := NewTestMux()

	root.Route("/api/v1", func(v1 cell.RouteMux) {
		v1.Route("/access", func(acc cell.RouteMux) {
			acc.Route("/sessions", func(sess cell.RouteMux) {
				auth.Declare(sess, auth.RouteDecl{
					Method:  "POST",
					Path:    "/login",
					Handler: okHandler,
					Public:  true,
				})
				auth.Declare(sess, auth.RouteDecl{
					Method:              "DELETE",
					Path:                "/{id}",
					Handler:             okHandler,
					Policy:              auth.Authenticated(),
					PasswordResetExempt: true,
				})
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
