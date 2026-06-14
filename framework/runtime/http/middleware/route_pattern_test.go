package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRouteFor_RecordedFromServeMux(t *testing.T) {
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
					captured = RouteFor(req.Context(), req.Method, req.URL.Path)
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

func TestRouteFor_UnmatchedRoute(t *testing.T) {
	var captured string
	capture := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			captured = RouteFor(req.Context(), req.Method, req.URL.Path)
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

func TestRouteFor_NilContext(t *testing.T) {
	got := RouteFor(context.Background(), http.MethodGet, "/any")
	assert.Equal(t, UnmatchedRoute, got)
}

func TestRouteFor_RecorderInstalledByOuterMiddleware(t *testing.T) {
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
		captured = RouteFor(r.Context(), r.Method, r.URL.Path)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/abc", nil)
	rec := httptest.NewRecorder()
	bare.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, UnmatchedRoute, captured,
		"without WithRoutePatternRecorder + dispatch wrapper, RouteFor returns sentinel")
}

func TestRouteFor_FallbackToRouteResolver(t *testing.T) {
	// RouteFor must fall back to RouteResolver when the dispatch-time
	// recorder has no pattern (short-circuit reject before dispatch).
	resolver := RouteResolver(func(method, urlPath string) (string, bool) {
		if method == http.MethodGet && urlPath == "/api/v1/users/42" {
			return "/api/v1/users/{id}", true
		}
		return "", false
	})

	ctx := WithRoutePatternRecorder(context.Background())
	ctx = WithRouteResolver(ctx, resolver)

	// Recorder is empty (short-circuit happened before dispatch).
	got := RouteFor(ctx, http.MethodGet, "/api/v1/users/42")
	assert.Equal(t, "/api/v1/users/{id}", got,
		"RouteFor must fall back to RouteResolver when recorder is empty")
}

func TestRouteFor_RecorderTakesPrecedenceOverResolver(t *testing.T) {
	// The dispatch-time recorder value must take priority over the resolver.
	resolver := RouteResolver(func(_, _ string) (string, bool) {
		return "/wrong/pattern", true
	})

	ctx := WithRoutePatternRecorder(context.Background())
	ctx = WithRouteResolver(ctx, resolver)
	RecordRoutePattern(ctx, "/correct/pattern")

	got := RouteFor(ctx, http.MethodGet, "/correct/123")
	assert.Equal(t, "/correct/pattern", got,
		"recorder value must take precedence over RouteResolver")
}

func TestRouteFor_ResolverReturnsUnmatchedWhenNoMatch(t *testing.T) {
	resolver := RouteResolver(func(_, _ string) (string, bool) {
		return "", false
	})

	ctx := WithRoutePatternRecorder(context.Background())
	ctx = WithRouteResolver(ctx, resolver)

	got := RouteFor(ctx, http.MethodGet, "/no-match")
	assert.Equal(t, UnmatchedRoute, got)
}
