package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
)

func TestWithBodyLimit(t *testing.T) {
	r := New(WithBodyLimit(1024))
	assert.Equal(t, int64(1024), r.bodyLimit)
}

func TestRouter_Handler(t *testing.T) {
	r := New()
	h := r.Handler()
	assert.NotNil(t, h)
	// Handler should be the underlying chi.Mux.
	assert.Equal(t, r.mux, h)
}

func TestRouteGroup_Route(t *testing.T) {
	r := New()
	r.Route("/api", func(mux kcell.RouteMux) {
		mux.Route("/v2", func(sub kcell.RouteMux) {
			sub.Handle("/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("pong"))
			}))
		})
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/ping", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pong", rec.Body.String())
}

func TestRouteGroup_Mount(t *testing.T) {
	r := New()
	r.Route("/api", func(mux kcell.RouteMux) {
		subHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("mounted"))
		})
		mux.Mount("/ext", subHandler)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ext", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouteGroup_Group(t *testing.T) {
	r := New()
	r.Route("/api", func(mux kcell.RouteMux) {
		mux.Group(func(sub kcell.RouteMux) {
			sub.Handle("/grouped", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("in-group"))
			}))
		})
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/grouped", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouteGroup_With(t *testing.T) {
	r := New()
	r.Route("/api", func(mux kcell.RouteMux) {
		authed := mux.With(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Custom", "applied")
				next.ServeHTTP(w, r)
			})
		})
		authed.Handle("/protected", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "applied", rec.Header().Get("X-Custom"))
}

func TestWith(t *testing.T) {
	r := New()
	authed := r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Root", "yes")
			next.ServeHTTP(w, req)
		})
	})
	authed.Handle("/root-mw", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/root-mw", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "yes", rec.Header().Get("X-Root"))
}
