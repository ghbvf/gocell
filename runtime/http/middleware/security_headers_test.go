package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "max-age=63072000", rec.Header().Get("Strict-Transport-Security"),
		"default SecurityHeaders should emit max-age only (no includeSubDomains/preload)")
}

func TestSecurityHeadersWithOptions(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name    string
		opts    []SecurityHeadersOption
		wantSTS string
	}{
		{
			name:    "default: max-age only",
			opts:    nil,
			wantSTS: "max-age=63072000",
		},
		{
			name:    "with includeSubDomains",
			opts:    []SecurityHeadersOption{WithHSTSIncludeSubDomains()},
			wantSTS: "max-age=63072000; includeSubDomains",
		},
		{
			name:    "with preload",
			opts:    []SecurityHeadersOption{WithHSTSPreload()},
			wantSTS: "max-age=63072000; preload",
		},
		{
			name:    "with both includeSubDomains and preload",
			opts:    []SecurityHeadersOption{WithHSTSIncludeSubDomains(), WithHSTSPreload()},
			wantSTS: "max-age=63072000; includeSubDomains; preload",
		},
		{
			name:    "custom max-age",
			opts:    []SecurityHeadersOption{WithHSTSMaxAge(31536000)},
			wantSTS: "max-age=31536000",
		},
		{
			name: "custom max-age with all directives",
			opts: []SecurityHeadersOption{
				WithHSTSMaxAge(31536000),
				WithHSTSIncludeSubDomains(),
				WithHSTSPreload(),
			},
			wantSTS: "max-age=31536000; includeSubDomains; preload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := SecurityHeadersWithOptions(tt.opts...)(noop)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantSTS, rec.Header().Get("Strict-Transport-Security"))
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
		})
	}
}
