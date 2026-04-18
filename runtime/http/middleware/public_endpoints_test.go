package middleware_test

// ref: Go 1.22 net/http ServeMux pattern grammar "[METHOD] PATH"
// ref: otelhttp WithPublicEndpointFn per-request predicate
// GET → HEAD alias rationale: RFC 7231 §4.3.2 — HEAD is identical to GET but
// without a response body; stdlib ServeMux and chi v5 both treat GET entries
// as implicitly covering HEAD.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompilePublicEndpoints_NormalCases(t *testing.T) {
	tests := []struct {
		name        string
		entries     []string
		matchMethod string
		matchPath   string
		wantMatch   bool
	}{
		{
			name:        "exact POST match",
			entries:     []string{"POST /api/v1/auth/login"},
			matchMethod: http.MethodPost,
			matchPath:   "/api/v1/auth/login",
			wantMatch:   true,
		},
		{
			name:        "POST entry does not match GET",
			entries:     []string{"POST /api/v1/auth/login"},
			matchMethod: http.MethodGet,
			matchPath:   "/api/v1/auth/login",
			wantMatch:   false,
		},
		{
			name:        "multi-space normalised by TrimSpace",
			entries:     []string{"POST   /foo"},
			matchMethod: http.MethodPost,
			matchPath:   "/foo",
			wantMatch:   true,
		},
		{
			name:        "lowercase method normalised to uppercase",
			entries:     []string{"post /foo"},
			matchMethod: http.MethodPost,
			matchPath:   "/foo",
			wantMatch:   true,
		},
		{
			name:        "path.Clean strips trailing slash",
			entries:     []string{"POST /foo/"},
			matchMethod: http.MethodPost,
			matchPath:   "/foo",
			wantMatch:   true,
		},
		{
			name:        "GET entry also matches HEAD (RFC 7231 alias)",
			entries:     []string{"GET /api/v1/.well-known/jwks"},
			matchMethod: http.MethodHead,
			matchPath:   "/api/v1/.well-known/jwks",
			wantMatch:   true,
		},
		{
			name:        "GET entry still matches GET",
			entries:     []string{"GET /api/v1/.well-known/jwks"},
			matchMethod: http.MethodGet,
			matchPath:   "/api/v1/.well-known/jwks",
			wantMatch:   true,
		},
		{
			name:        "path.Clean converges path traversal — no bypass expansion",
			entries:     []string{"GET /api/v1/foo"},
			matchMethod: http.MethodGet,
			matchPath:   "/api/../api/v1/foo",
			wantMatch:   true,
		},
		{
			name:        "non-matching method not bypassed",
			entries:     []string{"POST /api/v1/auth/login"},
			matchMethod: http.MethodDelete,
			matchPath:   "/api/v1/auth/login",
			wantMatch:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fn, err := middleware.CompilePublicEndpoints(tc.entries)
			require.NoError(t, err)

			req := httptest.NewRequest(tc.matchMethod, tc.matchPath, nil)
			assert.Equal(t, tc.wantMatch, fn(req))
		})
	}
}

func TestCompilePublicEndpoints_ErrorCases(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr string
	}{
		{
			name:    "no method prefix — path only",
			entries: []string{"/api/v1/auth/login"},
			wantErr: "public endpoint entry",
		},
		{
			name:    "empty string",
			entries: []string{""},
			wantErr: "public endpoint entry",
		},
		{
			name:    "method only, no path",
			entries: []string{"POST"},
			wantErr: "public endpoint entry",
		},
		{
			name:    "path does not start with slash",
			entries: []string{"POST api/v1/auth/login"},
			wantErr: "public endpoint entry",
		},
		{
			name:    "duplicate entry returns error",
			entries: []string{"POST /api/v1/auth/login", "POST /api/v1/auth/login"},
			wantErr: "duplicate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fn, err := middleware.CompilePublicEndpoints(tc.entries)
			require.Error(t, err, "expected error for entry %v", tc.entries)
			assert.Nil(t, fn)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestCompilePublicEndpoints_EmptySlice(t *testing.T) {
	// Empty slice: valid — no public endpoints, all requests return false.
	fn, err := middleware.CompilePublicEndpoints([]string{})
	require.NoError(t, err)
	require.NotNil(t, fn)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	assert.False(t, fn(req))
}

func TestCompilePublicEndpoints_MultipleEntries(t *testing.T) {
	entries := []string{
		"POST /api/v1/auth/login",
		"POST /api/v1/auth/refresh",
		"GET /api/v1/.well-known/jwks",
	}
	fn, err := middleware.CompilePublicEndpoints(entries)
	require.NoError(t, err)

	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/api/v1/auth/login", true},
		{http.MethodPost, "/api/v1/auth/refresh", true},
		{http.MethodGet, "/api/v1/.well-known/jwks", true},
		{http.MethodHead, "/api/v1/.well-known/jwks", true}, // GET alias
		{http.MethodGet, "/api/v1/auth/login", false},       // only POST allowed
		{http.MethodPut, "/api/v1/auth/login", false},
		{http.MethodDelete, "/api/v1/auth/refresh", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		assert.Equal(t, c.want, fn(req), "%s %s", c.method, c.path)
	}
}
