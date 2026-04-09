package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
)

// ---------------------------------------------------------------------------
// Contract tests for Mount / Route / Group behavior.
//
// These tests document the routing contract that any future router
// implementation must satisfy.  They exercise chi-backed behavior today and
// serve as a regression safety net for router replacement.
// ---------------------------------------------------------------------------

// --- Mount Prefix Stripping ------------------------------------------------

func TestMount_PrefixStripping(t *testing.T) {
	// Mount strips the prefix so the sub-router's patterns are relative to the
	// mount point.  Registering GET /users in a sub-router mounted at /api
	// means a request to /api/users reaches the handler.

	tests := []struct {
		name        string
		mountPrefix string
		subPattern  string // pattern registered in sub-router
		requestPath string
		wantStatus  int
		wantBody    string
	}{
		{
			name:        "sub-path is stripped and matched",
			mountPrefix: "/api",
			subPattern:  "/users",
			requestPath: "/api/users",
			wantStatus:  http.StatusOK,
			wantBody:    "ok:users",
		},
		{
			name:        "mount root matches /",
			mountPrefix: "/api",
			subPattern:  "/",
			requestPath: "/api",
			wantStatus:  http.StatusOK,
			wantBody:    "ok:root",
		},
		{
			name:        "trailing slash on mount root matches /",
			mountPrefix: "/api",
			subPattern:  "/",
			requestPath: "/api/",
			wantStatus:  http.StatusOK,
			wantBody:    "ok:root",
		},
		{
			name:        "unregistered sub-path returns 404",
			mountPrefix: "/api",
			subPattern:  "/users",
			requestPath: "/api/orders",
			wantStatus:  http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New()

			sub := chi.NewRouter()
			body := tt.wantBody
			sub.Get(tt.subPattern, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			})
			r.Mount(tt.mountPrefix, sub)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			r.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantBody != "" && rec.Code == http.StatusOK {
				assert.Equal(t, tt.wantBody, rec.Body.String())
			}
		})
	}
}

// --- Mount Middleware Inheritance -------------------------------------------

func TestMount_MiddlewareInheritance(t *testing.T) {
	r := New() // New() applies default middleware (RequestID, SecurityHeaders, etc.)

	// Add a custom middleware via Use() AFTER construction to verify it also
	// propagates to mounted handlers (not just the built-in middleware).
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Custom-MW", "applied")
			next.ServeHTTP(w, req)
		})
	})

	sub := chi.NewRouter()
	sub.Get("/resource", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Mount("/api", sub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/resource", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Built-in middleware from New().
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"),
		"RequestID middleware must run for mounted handlers")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"),
		"SecurityHeaders middleware must run for mounted handlers")
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"),
		"SecurityHeaders middleware must run for mounted handlers")
	// Middleware added via Use() after construction.
	assert.Equal(t, "applied", rec.Header().Get("X-Custom-MW"),
		"middleware added via Use() must propagate to mounted handlers")
}

// --- Route Prefix Stripping ------------------------------------------------

func TestRoute_PrefixStripping(t *testing.T) {
	// Route creates a sub-router whose patterns are relative to the route
	// prefix.  Registering "GET /users" inside Route("/api/v1", ...) means
	// a request to /api/v1/users reaches the handler.

	r := New()

	var handlerCalled bool
	r.Route("/api/v1", func(mux cell.RouteMux) {
		mux.Handle("GET /users", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("v1-users"))
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, handlerCalled, "Route must match sub-path relative to prefix")
	assert.Equal(t, "v1-users", rec.Body.String())

	// Verify the path without prefix does NOT match.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/users", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"handler registered inside Route must not be reachable at root level")
}

// --- Group No Prefix Change ------------------------------------------------

func TestGroup_NoPrefixChange(t *testing.T) {
	r := New()

	var handlerCalled bool
	r.Group(func(mux cell.RouteMux) {
		mux.Handle("GET /users", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			handlerCalled = true
			// Group does not change the path, handler sees the original URL.
			assert.Equal(t, "/users", req.URL.Path)
			w.WriteHeader(http.StatusOK)
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "Group must not add or remove prefix")
	assert.True(t, handlerCalled)
}

// --- Group Middleware Isolation ---------------------------------------------

func TestGroup_MiddlewareIsolation(t *testing.T) {
	// Middleware added inside a Group via Use() must not leak to handlers
	// outside the group.
	r := New()

	marker := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Group-Marker", "applied")
			next.ServeHTTP(w, req)
		})
	}

	r.Group(func(mux cell.RouteMux) {
		mux.Use(marker)
		mux.Handle("GET /inside", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	r.Handle("GET /outside", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Inside group: marker header present.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/inside", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "applied", rec.Header().Get("X-Group-Marker"))

	// Outside group: marker header absent.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/outside", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("X-Group-Marker"),
		"middleware inside Group must not leak to handlers outside the group")
}

// --- 404 / 405 Table-Driven -----------------------------------------------

func TestRouter_NotFound(t *testing.T) {
	r := New()
	r.Handle("GET /exists", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"registered path returns 200", http.MethodGet, "/exists", http.StatusOK},
		{"unregistered path returns 404", http.MethodGet, "/notexists", http.StatusNotFound},
		{"unregistered nested path returns 404", http.MethodGet, "/exists/nested", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestRouter_MethodNotAllowed(t *testing.T) {
	r := New()
	r.Handle("POST /submit", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		method string
		want   int
	}{
		{"correct method returns 200", http.MethodPost, http.StatusOK},
		{"wrong method returns 405", http.MethodGet, http.StatusMethodNotAllowed},
		{"wrong method PUT returns 405", http.MethodPut, http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/submit", nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// --- Subtree 404 / 405 ----------------------------------------------------

func TestRoute_NotFoundAndMethodNotAllowed(t *testing.T) {
	r := New()
	r.Route("/api", func(mux cell.RouteMux) {
		mux.Handle("GET /users", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"registered path returns 200", http.MethodGet, "/api/users", http.StatusOK},
		{"unregistered sub-path returns 404", http.MethodGet, "/api/orders", http.StatusNotFound},
		{"wrong method returns 405", http.MethodPost, "/api/users", http.StatusMethodNotAllowed},
		{"outside subtree returns 404", http.MethodGet, "/other", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestMount_NotFoundAndMethodNotAllowed(t *testing.T) {
	r := New()
	sub := chi.NewRouter()
	sub.Get("/items", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Mount("/store", sub)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"registered path returns 200", http.MethodGet, "/store/items", http.StatusOK},
		{"unregistered sub-path returns 404", http.MethodGet, "/store/nope", http.StatusNotFound},
		{"wrong method returns 405", http.MethodPost, "/store/items", http.StatusMethodNotAllowed},
		{"outside mount returns 404", http.MethodGet, "/other", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// --- Nested Mount ----------------------------------------------------------

func TestMount_Nested(t *testing.T) {
	r := New()

	// Inner sub-router mounted at /v1 inside the outer sub-router at /api.
	// The handler's pattern is relative to the innermost mount point.
	inner := chi.NewRouter()
	inner.Get("/resource", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("inner-resource"))
	})
	inner.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("inner-root"))
	})

	outer := chi.NewRouter()
	outer.Mount("/v1", inner)

	r.Mount("/api", outer)

	tests := []struct {
		name        string
		requestPath string
		wantStatus  int
		wantBody    string
	}{
		{"nested mount matches deep path", "/api/v1/resource", http.StatusOK, "inner-resource"},
		{"nested mount root", "/api/v1", http.StatusOK, "inner-root"},
		{"nested mount trailing slash", "/api/v1/", http.StatusOK, "inner-root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			r.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, rec.Body.String())
			}
		})
	}
}

// --- Mount with Route Params -----------------------------------------------

func TestMount_WithRouteParams(t *testing.T) {
	r := New()

	sub := chi.NewRouter()
	sub.Get("/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		w.Header().Set("X-Param-ID", id)
		_, _ = fmt.Fprintf(w, "id=%s", id)
	})
	r.Mount("/users", sub)

	tests := []struct {
		name   string
		path   string
		wantID string
	}{
		{"numeric id", "/users/123", "123"},
		{"string id", "/users/abc", "abc"},
		{"uuid-like id", "/users/550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantID, rec.Header().Get("X-Param-ID"),
				"mounted sub-handler must receive route params")
		})
	}
}
