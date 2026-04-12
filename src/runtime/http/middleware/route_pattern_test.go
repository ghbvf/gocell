package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
)

func TestRoutePattern_WithChiRouter(t *testing.T) {
	tests := []struct {
		name        string
		pattern     string
		requestPath string
		wantRoute   string
	}{
		{
			name:        "static path",
			pattern:     "/api/v1/health",
			requestPath: "/api/v1/health",
			wantRoute:   "/api/v1/health",
		},
		{
			name:        "single param",
			pattern:     "/api/v1/users/{id}",
			requestPath: "/api/v1/users/123",
			wantRoute:   "/api/v1/users/{id}",
		},
		{
			name:        "multiple params",
			pattern:     "/api/v1/users/{userID}/posts/{postID}",
			requestPath: "/api/v1/users/42/posts/99",
			wantRoute:   "/api/v1/users/{userID}/posts/{postID}",
		},
		{
			name:        "wildcard",
			pattern:     "/files/*",
			requestPath: "/files/a/b/c.txt",
			wantRoute:   "/files/*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// RoutePatternFromCtx only works when middleware is inside the chi
			// router (via r.Use), because chi sets RouteContext on a request
			// copy. This is the actual usage pattern in router.go.
			var captured string
			r := chi.NewRouter()
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					next.ServeHTTP(w, req)
					captured = RoutePatternFromCtx(req.Context())
				})
			})
			r.Get(tt.pattern, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantRoute, captured)
		})
	}
}

func TestRoutePattern_UnmatchedRoute(t *testing.T) {
	var captured string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			captured = RoutePatternFromCtx(req.Context())
		})
	})
	r.Get("/exists", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, UnmatchedRoute, captured)
}

func TestRoutePattern_NilContext(t *testing.T) {
	got := RoutePatternFromCtx(context.Background())
	assert.Equal(t, UnmatchedRoute, got)
}

func TestRoutePattern_MiddlewareInsideChiRouter(t *testing.T) {
	// When middleware is registered via r.Use(), chi has already set
	// RouteContext on the request before calling middleware. The RouteContext
	// is a pointer, so RoutePatterns populated during routing are visible
	// in the middleware after next.ServeHTTP().
	var captured string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			captured = RoutePatternFromCtx(req.Context())
		})
	})
	r.Get("/api/v1/devices/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/abc", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/api/v1/devices/{id}", captured)
}
