package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSessionConfig(t *testing.T) CookieSessionConfig {
	t.Helper()
	secret := generateKey(t, 32)
	return DefaultCookieSessionConfig(secret)
}

func newTestSessionConfigEncrypted(t *testing.T) CookieSessionConfig {
	t.Helper()
	cfg := newTestSessionConfig(t)
	cfg.EncryptKey = generateKey(t, 32)
	return cfg
}

// encodeCookieValue encodes a JWT string into a signed cookie value.
func encodeCookieValue(t *testing.T, cfg CookieSessionConfig, jwt string) string {
	t.Helper()
	sc, err := NewSecureCookie(cfg.Secret, cfg.EncryptKey)
	require.NoError(t, err)
	name := cfg.CookieName
	if name == "" {
		name = "session"
	}
	if cfg.MaxAge > 0 {
		sc = sc.WithMaxAge(cfg.MaxAge)
	}
	encoded, err := sc.Encode(name, []byte(jwt))
	require.NoError(t, err)
	return encoded
}

// authCapture records the Authorization header seen by downstream handler.
type authCapture struct {
	authHeader string
	called     bool
}

func (ac *authCapture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac.authHeader = r.Header.Get("Authorization")
		ac.called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestCookieSession_ValidCookie_InjectsAuthorization(t *testing.T) {
	cfg := newTestSessionConfig(t)
	jwt := "eyJhbGciOiJSUzI1NiJ9.test-payload.signature"
	cookieVal := encodeCookieValue(t, cfg, jwt)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: cookieVal})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Equal(t, "Bearer "+jwt, capture.authHeader)
}

func TestCookieSession_ExpiredCookie_NoInjection(t *testing.T) {
	cfg := newTestSessionConfig(t)
	cfg.MaxAge = 1 // 1 second — cookie expires after 1s

	// Encode with the SAME maxAge so the timestamp is embedded.
	sc, err := NewSecureCookie(cfg.Secret, nil)
	require.NoError(t, err)
	encoded, err := sc.Encode("session", []byte("jwt-token"))
	require.NoError(t, err)

	// Wait for expiry.
	time.Sleep(1100 * time.Millisecond)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: encoded})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Empty(t, capture.authHeader, "expired cookie should NOT inject Authorization")
}

func TestCookieSession_TamperedCookie_NoInjection(t *testing.T) {
	cfg := newTestSessionConfig(t)
	cookieVal := encodeCookieValue(t, cfg, "valid-jwt")

	tampered := cookieVal[:len(cookieVal)/2] + "XXXX" + cookieVal[len(cookieVal)/2+4:]

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: tampered})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Empty(t, capture.authHeader, "tampered cookie should not inject auth")
}

func TestCookieSession_NoCookie_AuthorizationPresent_PassThrough(t *testing.T) {
	cfg := newTestSessionConfig(t)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.Header.Set("Authorization", "Bearer existing-jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Equal(t, "Bearer existing-jwt", capture.authHeader, "existing auth should pass through")
}

func TestCookieSession_NoCookie_NoAuthorization_PassThrough(t *testing.T) {
	cfg := newTestSessionConfig(t)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Empty(t, capture.authHeader, "no cookie, no auth — nothing injected")
}

func TestCookieSession_BothCookieAndAuthorization_AuthorizationWins(t *testing.T) {
	cfg := newTestSessionConfig(t)
	cookieVal := encodeCookieValue(t, cfg, "cookie-jwt")

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.Header.Set("Authorization", "Bearer header-jwt")
	req.AddCookie(&http.Cookie{Name: "session", Value: cookieVal})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Equal(t, "Bearer header-jwt", capture.authHeader, "Authorization header should take priority")
}

func TestSetSessionCookie_Attributes(t *testing.T) {
	cfg := newTestSessionConfig(t)
	cfg.CookieDomain = "example.com"

	rec := httptest.NewRecorder()
	err := SetSessionCookie(rec, cfg, "my-jwt-token")
	require.NoError(t, err)

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)

	c := cookies[0]
	assert.Equal(t, "session", c.Name)
	assert.NotEmpty(t, c.Value)
	assert.Equal(t, "/", c.Path)
	assert.Equal(t, "example.com", c.Domain)
	assert.Equal(t, 900, c.MaxAge)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
}

func TestSetSessionCookie_ReturnsError(t *testing.T) {
	cfg := CookieSessionConfig{
		Secret: []byte("too-short"), // < 32 bytes
	}
	rec := httptest.NewRecorder()
	err := SetSessionCookie(rec, cfg, "jwt")
	assert.Error(t, err, "should return error for invalid config")
}

func TestClearSessionCookie(t *testing.T) {
	cfg := newTestSessionConfig(t)

	rec := httptest.NewRecorder()
	ClearSessionCookie(rec, cfg)

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)

	c := cookies[0]
	assert.Equal(t, "session", c.Name)
	assert.Empty(t, c.Value)
	assert.Equal(t, -1, c.MaxAge, "MaxAge=-1 deletes the cookie")
	assert.True(t, c.HttpOnly)
}

func TestCookieSession_EncryptedMode_RoundTrip(t *testing.T) {
	cfg := newTestSessionConfigEncrypted(t)
	jwt := "encrypted-jwt-payload"
	cookieVal := encodeCookieValue(t, cfg, jwt)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: cookieVal})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Equal(t, "Bearer "+jwt, capture.authHeader)
}

func TestDefaultCookieSessionConfig(t *testing.T) {
	secret := generateKey(t, 32)
	cfg := DefaultCookieSessionConfig(secret)

	assert.Equal(t, secret, cfg.Secret)
	assert.Equal(t, "session", cfg.CookieName)
	assert.Equal(t, "/", cfg.CookiePath)
	assert.True(t, cfg.CookieSecure)
	assert.Equal(t, http.SameSiteStrictMode, cfg.CookieSameSite)
	assert.Equal(t, 900, cfg.MaxAge)
}

func TestSetSessionCookie_RoundTripViaMiddleware(t *testing.T) {
	cfg := newTestSessionConfig(t)

	rec1 := httptest.NewRecorder()
	err := SetSessionCookie(rec1, cfg, "round-trip-jwt")
	require.NoError(t, err)
	cookies := rec1.Result().Cookies()
	require.Len(t, cookies, 1)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)

	assert.True(t, capture.called)
	assert.Equal(t, "Bearer round-trip-jwt", capture.authHeader)
}

func TestCookieSession_LargeJWT(t *testing.T) {
	cfg := newTestSessionConfig(t)
	largeJWT := make([]byte, 4000)
	for i := range largeJWT {
		largeJWT[i] = 'A' + byte(i%26)
	}
	jwt := string(largeJWT)
	cookieVal := encodeCookieValue(t, cfg, jwt)

	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: cookieVal})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, capture.called)
	assert.Equal(t, "Bearer "+jwt, capture.authHeader)
}

func TestSessionCookieWriter_SetAndClear(t *testing.T) {
	cfg := newTestSessionConfig(t)
	writer, err := NewSessionCookieWriter(cfg)
	require.NoError(t, err)

	// Set
	rec := httptest.NewRecorder()
	err = writer.Set(rec, "writer-jwt")
	require.NoError(t, err)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "session", cookies[0].Name)
	assert.NotEmpty(t, cookies[0].Value)

	// Verify cookie works with middleware.
	capture := &authCapture{}
	handler := CookieSession(cfg)(capture.handler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	assert.Equal(t, "Bearer writer-jwt", capture.authHeader)

	// Clear
	rec3 := httptest.NewRecorder()
	writer.Clear(rec3)
	clearCookies := rec3.Result().Cookies()
	require.Len(t, clearCookies, 1)
	assert.Equal(t, -1, clearCookies[0].MaxAge)
}

func TestNormalizeCookieSessionConfig(t *testing.T) {
	// Struct literal with zero values should get safe defaults.
	cfg := CookieSessionConfig{
		Secret: generateKey(t, 32),
	}
	normalizeCookieSessionConfig(&cfg)

	assert.Equal(t, "session", cfg.CookieName)
	assert.Equal(t, "/", cfg.CookiePath)
	assert.Equal(t, http.SameSiteStrictMode, cfg.CookieSameSite)
	assert.Equal(t, 900, cfg.MaxAge)
}
