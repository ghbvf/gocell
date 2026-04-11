package celltest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
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
