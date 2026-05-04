package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoutePattern_RecordedFromServeMux(t *testing.T) {
	tests := []struct {
		name        string
		pattern     string
		requestPath string
		wantRoute   string
	}{
		{
			name:        "static path",
			pattern:     "GET /api/v1/health",
			requestPath: "/api/v1/health",
			wantRoute:   "/api/v1/health",
		},
		{
			name:        "single param",
			pattern:     "GET /api/v1/users/{id}",
			requestPath: "/api/v1/users/123",
			wantRoute:   "/api/v1/users/{id}",
		},
		{
			name:        "multiple params",
			pattern:     "GET /api/v1/users/{userID}/posts/{postID}",
			requestPath: "/api/v1/users/42/posts/99",
			wantRoute:   "/api/v1/users/{userID}/posts/{postID}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured string
			capture := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					next.ServeHTTP(w, req)
					captured = RoutePatternFromCtx(req.Context())
				})
			}
			handler := buildTestServer(
				[]func(http.Handler) http.Handler{capture},
				func(mux *http.ServeMux) {
					mux.Handle(tt.pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(http.StatusOK)
					}))
				},
			)

			req := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantRoute, captured)
		})
	}
}

func TestRoutePattern_UnmatchedRoute(t *testing.T) {
	var captured string
	capture := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			captured = RoutePatternFromCtx(req.Context())
		})
	}
	handler := buildTestServer(
		[]func(http.Handler) http.Handler{capture},
		func(mux *http.ServeMux) {
			mux.Handle("GET /exists", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, UnmatchedRoute, captured)
}

func TestRoutePattern_NilContext(t *testing.T) {
	got := RoutePatternFromCtx(context.Background())
	assert.Equal(t, UnmatchedRoute, got)
}

func TestRoutePattern_RecorderInstalledByOuterMiddleware(t *testing.T) {
	// Regression test: without the outer pattern-recorder middleware, the
	// dispatch wrapper has nowhere to write and middleware sees the
	// sentinel — confirming the recorder is the single source of truth.
	var captured string
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/devices/{id}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	bare := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		captured = RoutePatternFromCtx(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/abc", nil)
	rec := httptest.NewRecorder()
	bare.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, UnmatchedRoute, captured,
		"without WithRoutePatternRecorder + dispatch wrapper, RoutePatternFromCtx returns sentinel")
}
