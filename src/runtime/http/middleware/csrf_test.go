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

func TestCSRF_UnsafeMethods(t *testing.T) {
	// All unsafe methods should be subject to CSRF validation.
	cfg := CSRFConfig{
		TrustedOrigins: []string{"https://example.com"},
	}
	handler := csrfMiddleware(cfg)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method+"_allowed", func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/data", nil)
			req.Header.Set("Origin", "https://example.com")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
		t.Run(method+"_rejected", func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/data", nil)
			req.Header.Set("Origin", "https://evil.com")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

func TestCSRF_ExcludedPaths(t *testing.T) {
	cfg := CSRFConfig{
		ExcludedPathPrefixes: []string{"/healthz", "/readyz", "/api/webhooks/"},
		AllowMissingOrigin:   true,
	}
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

func TestCSRF_ExcludedPaths_PathTraversal(t *testing.T) {
	// path.Clean prevents /api/webhooks/../secret from matching /api/webhooks/.
	cfg := CSRFConfig{
		ExcludedPathPrefixes: []string{"/api/webhooks/"},
	}
	handler := csrfMiddleware(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/../secret", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"path traversal should not bypass CSRF via excluded prefix")
}

func TestCSRF_SecFetchSite(t *testing.T) {
	tests := []struct {
		name          string
		secFetchSite  string
		allowSameSite bool
		trusted       []string
		origin        string
		expect        int
	}{
		{"same-origin allows", "same-origin", false, nil, "", 200},
		{"none allows (direct navigation)", "none", false, nil, "", 200},
		{"cross-site rejects", "cross-site", true, nil, "", 403},
		{"bogus value rejects", "unknown-value", true, nil, "", 403},
		{"same-site rejects when not allowed", "same-site", false, nil, "", 403},
		// P0 fix: same-site + AllowSameSite=true falls through to Origin check.
		{"same-site with trusted origin allowed", "same-site", true,
			[]string{"https://example.com"}, "https://example.com", 200},
		{"same-site with untrusted origin rejected", "same-site", true,
			[]string{"https://example.com"}, "https://evil.example.com", 403},
		{"same-site without origin signal rejected (fail-closed)", "same-site", true,
			[]string{"https://example.com"}, "", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CSRFConfig{
				AllowSameSite:      tt.allowSameSite,
				AllowMissingOrigin: false,
				TrustedOrigins:     tt.trusted,
			}
			handler := csrfMiddleware(cfg)

			req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
			req.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
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
		{"null origin rejected", "null", 403},
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

	assert.Equal(t, http.StatusForbidden, rec.Code, "Sec-Fetch-Site=cross-site should reject even with trusted Origin")
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
	assert.False(t, cfg.AllowSameSite, "default should be fail-closed for same-site")
	assert.False(t, cfg.AllowMissingOrigin, "default should be fail-closed for missing origin")
}

func TestCSRF_CookieSessionIntegration(t *testing.T) {
	// Test the CSRF → CookieSession middleware chain.
	secret := generateKey(t, 32)
	sessCfg := DefaultCookieSessionConfig(secret)

	csrfCfg := CSRFConfig{
		TrustedOrigins:     []string{"https://app.example.com"},
		AllowMissingOrigin: false,
	}

	// Build chain: CSRF → CookieSession → capture handler.
	capture := &authCapture{}
	chain := CSRF(csrfCfg)(MustCookieSession(sessCfg)(capture.handler()))

	// Encode a JWT into a session cookie.
	jwt := "test-jwt-token"
	cookieVal := encodeCookieValue(t, sessCfg, jwt)

	t.Run("trusted origin + valid cookie → injects auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.AddCookie(&http.Cookie{Name: "session", Value: cookieVal})
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "Bearer "+jwt, capture.authHeader)
	})

	t.Run("untrusted origin → CSRF blocks", func(t *testing.T) {
		capture.authHeader = ""
		capture.called = false

		req := httptest.NewRequest(http.MethodPost, "/api/data", nil)
		req.Header.Set("Origin", "https://evil.com")
		req.AddCookie(&http.Cookie{Name: "session", Value: cookieVal})
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, capture.called, "handler should not be called when CSRF rejects")
	})
}
