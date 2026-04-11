package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// okHandler writes 200 for tests.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func csrfMiddleware(cfg CSRFConfig) http.Handler {
	return CSRF(cfg)(okHandler)
}

func TestCSRF_SafeMethods(t *testing.T) {
	handler := csrfMiddleware(CSRFConfig{})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/data", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestCSRF_ExcludedPaths(t *testing.T) {
	cfg := DefaultCSRFConfig()
	cfg.ExcludedPathPrefixes = []string{"/healthz", "/readyz", "/api/webhooks/"}
	handler := csrfMiddleware(cfg)

	tests := []struct {
		name   string
		path   string
		expect int
	}{
		{"healthz", "/healthz", 200},
		{"readyz", "/readyz", 200},
		{"webhook", "/api/webhooks/stripe", 200},
		{"non-excluded uses default (missing origin allowed)", "/api/data", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expect, rec.Code)
		})
	}
}

func TestCSRF_SecFetchSite(t *testing.T) {
	tests := []struct {
		name           string
		secFetchSite   string
		allowSameSite  bool
		expect         int
	}{
		{"same-origin allows", "same-origin", true, 200},
		{"same-site allows when configured", "same-site", true, 200},
		{"same-site rejects when not configured", "same-site", false, 403},
		{"none allows (direct navigation)", "none", true, 200},
		{"cross-site rejects", "cross-site", true, 403},
		{"bogus value rejects", "unknown-value", true, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CSRFConfig{AllowSameSite: tt.allowSameSite, AllowMissingOrigin: true}
			handler := csrfMiddleware(cfg)

			req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
			req.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expect, rec.Code)
		})
	}
}

func TestCSRF_OriginHeader(t *testing.T) {
	cfg := CSRFConfig{
		TrustedOrigins:     []string{"https://example.com", "https://admin.example.com:8443"},
		AllowMissingOrigin: false,
	}
	handler := csrfMiddleware(cfg)

	tests := []struct {
		name   string
		origin string
		expect int
	}{
		{"matching origin", "https://example.com", 200},
		{"non-matching origin", "https://evil.com", 403},
		{"matching origin with port", "https://admin.example.com:8443", 200},
		{"case-insensitive", "https://Example.COM", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
			req.Header.Set("Origin", tt.origin)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expect, rec.Code)
		})
	}
}

func TestCSRF_WildcardOrigin(t *testing.T) {
	cfg := CSRFConfig{
		TrustedOrigins:     []string{"https://*.example.com"},
		AllowMissingOrigin: false,
	}
	handler := csrfMiddleware(cfg)

	tests := []struct {
		name   string
		origin string
		expect int
	}{
		{"subdomain matches", "https://sub.example.com", 200},
		{"deep subdomain matches", "https://a.b.example.com", 200},
		{"bare domain does not match wildcard", "https://example.com", 403},
		{"different domain rejects", "https://evil.com", 403},
		{"different scheme rejects", "http://sub.example.com", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
			req.Header.Set("Origin", tt.origin)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expect, rec.Code)
		})
	}
}

func TestCSRF_RefererFallback(t *testing.T) {
	cfg := CSRFConfig{
		TrustedOrigins:     []string{"https://example.com"},
		AllowMissingOrigin: false,
	}
	handler := csrfMiddleware(cfg)

	tests := []struct {
		name    string
		referer string
		expect  int
	}{
		{"matching referer", "https://example.com/page?q=1", 200},
		{"non-matching referer", "https://evil.com/page", 403},
		{"malformed referer", "not-a-url", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
			// No Origin, no Sec-Fetch-Site — falls back to Referer.
			req.Header.Set("Referer", tt.referer)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expect, rec.Code)
		})
	}
}

func TestCSRF_MissingHeaders(t *testing.T) {
	tests := []struct {
		name               string
		allowMissingOrigin bool
		expect             int
	}{
		{"permissive mode allows", true, 200},
		{"strict mode rejects", false, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CSRFConfig{AllowMissingOrigin: tt.allowMissingOrigin}
			handler := csrfMiddleware(cfg)

			req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
			// No Sec-Fetch-Site, no Origin, no Referer.
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tt.expect, rec.Code)
		})
	}
}

func TestCSRF_ErrorResponseFormat(t *testing.T) {
	cfg := CSRFConfig{AllowMissingOrigin: false}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)

	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "response must have error object")
	assert.Equal(t, "ERR_CSRF_ORIGIN_DENIED", errObj["code"])
	assert.Equal(t, "cross-origin request denied", errObj["message"])
	assert.Equal(t, map[string]any{}, errObj["details"])
}

func TestCSRF_ErrorResponseIncludesRequestID(t *testing.T) {
	cfg := CSRFConfig{AllowMissingOrigin: false}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	ctx := ctxkeys.WithRequestID(req.Context(), "req-123")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "req-123", errObj["request_id"])
}

func TestCSRF_VaryHeader(t *testing.T) {
	cfg := CSRFConfig{
		TrustedOrigins:     []string{"https://example.com"},
		AllowMissingOrigin: true,
	}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Vary"), "Origin")
}

func TestCSRF_SecFetchSitePriority(t *testing.T) {
	// Sec-Fetch-Site=cross-site should reject even if Origin is trusted.
	cfg := CSRFConfig{
		TrustedOrigins:     []string{"https://example.com"},
		AllowMissingOrigin: false,
	}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "Sec-Fetch-Site should take priority over Origin")
}

func TestCSRF_MultipleTrustedOrigins(t *testing.T) {
	cfg := CSRFConfig{
		TrustedOrigins:     []string{"https://a.com", "https://b.com", "https://c.com"},
		AllowMissingOrigin: false,
	}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	req.Header.Set("Origin", "https://b.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCSRF_EmptyTrustedOrigins(t *testing.T) {
	cfg := CSRFConfig{
		TrustedOrigins:     []string{},
		AllowMissingOrigin: false,
	}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "no trusted origins means nothing can match")
}

func TestCSRF_DefaultConfig(t *testing.T) {
	cfg := DefaultCSRFConfig()
	assert.True(t, cfg.AllowSameSite)
	assert.True(t, cfg.AllowMissingOrigin)
}
