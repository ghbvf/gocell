package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// ref: alexedwards/scs — LoadAndSave middleware pattern
// ref: gorilla/sessions — CookieStore dual-mode (cookie + header)

// CookieSessionConfig configures the BFF cookie session middleware.
type CookieSessionConfig struct {
	// Secret is the HMAC key for SecureCookie signing (≥32 bytes, required).
	Secret []byte

	// EncryptKey is the AES key for cookie encryption.
	// nil = signing only, 16/24/32 bytes = AES-128/192/256-GCM.
	EncryptKey []byte

	// CookieName is the session cookie name. Default: "session".
	CookieName string

	// CookiePath is the cookie path. Default: "/".
	CookiePath string

	// CookieDomain is the cookie domain. Default: "" (current domain).
	CookieDomain string

	// CookieSecure sets the Secure flag. Default: true.
	CookieSecure bool

	// CookieSameSite sets the SameSite attribute. Default: Strict.
	CookieSameSite http.SameSite

	// MaxAge is the cookie max age in seconds. Default: 900 (15min, matches JWT TTL).
	MaxAge int
}

// DefaultCookieSessionConfig returns a CookieSessionConfig with safe defaults.
func DefaultCookieSessionConfig(secret []byte) CookieSessionConfig {
	return CookieSessionConfig{
		Secret:         secret,
		CookieName:     "session",
		CookiePath:     "/",
		CookieSecure:   true,
		CookieSameSite: http.SameSiteStrictMode,
		MaxAge:         900,
	}
}

// normalizeCookieSessionConfig fills zero-value fields with safe defaults.
func normalizeCookieSessionConfig(cfg *CookieSessionConfig) {
	if cfg.CookieName == "" {
		cfg.CookieName = "session"
	}
	if cfg.CookiePath == "" {
		cfg.CookiePath = "/"
	}
	if cfg.CookieSameSite == 0 {
		cfg.CookieSameSite = http.SameSiteStrictMode
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 900
	}
	// CookieSecure: false zero-value is intentionally not overridden here.
	// Users constructing via struct literal get Secure=false, which is only
	// safe for local development. Production code MUST use
	// DefaultCookieSessionConfig or explicitly set CookieSecure=true.
}

// CookieSession returns middleware that reads a JWT from a signed cookie and
// injects it as an Authorization: Bearer header. If the request already has
// an Authorization header, the cookie is ignored (API client mode).
//
// This middleware does NOT set cookies — use SetSessionCookie/ClearSessionCookie
// in login/logout handlers.
func CookieSession(cfg CookieSessionConfig) func(http.Handler) http.Handler {
	normalizeCookieSessionConfig(&cfg)

	// Build SecureCookie instance at construction time.
	sc, err := NewSecureCookie(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		// Fail-fast: configuration error should surface immediately.
		panic("cookie_session: invalid config: " + err.Error())
	}
	if cfg.MaxAge > 0 {
		sc = sc.WithMaxAge(cfg.MaxAge)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If Authorization header already present, skip cookie processing.
			if auth := r.Header.Get("Authorization"); auth != "" &&
				strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				next.ServeHTTP(w, r)
				return
			}

			// Try to read session cookie.
			cookie, err := r.Cookie(cfg.CookieName)
			if err != nil || cookie.Value == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Decode and verify cookie.
			jwt, err := sc.Decode(cfg.CookieName, cookie.Value)
			if err != nil {
				// Invalid/expired cookie — do not inject, let AuthMiddleware handle 401.
				next.ServeHTTP(w, r)
				return
			}

			// Inject JWT as Authorization header for downstream AuthMiddleware.
			r2 := r.Clone(r.Context())
			r2.Header.Set("Authorization", "Bearer "+string(jwt))
			next.ServeHTTP(w, r2)
		})
	}
}

// NewSessionCookieWriter creates a reusable writer for setting session cookies.
// Pre-builds the SecureCookie instance to avoid per-call reconstruction.
// Use this in login/refresh handlers instead of calling SetSessionCookie
// directly with config each time.
func NewSessionCookieWriter(cfg CookieSessionConfig) (*SessionCookieWriter, error) {
	normalizeCookieSessionConfig(&cfg)

	sc, err := NewSecureCookie(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		return nil, fmt.Errorf("cookie_session: %w", err)
	}

	return &SessionCookieWriter{sc: sc, cfg: cfg}, nil
}

// SessionCookieWriter writes and clears session cookies using a pre-built
// SecureCookie instance for consistent performance.
type SessionCookieWriter struct {
	sc  *SecureCookie
	cfg CookieSessionConfig
}

// Set writes a signed (optionally encrypted) JWT cookie to the response.
func (w *SessionCookieWriter) Set(rw http.ResponseWriter, jwt string) error {
	encoded, err := w.sc.Encode(w.cfg.CookieName, []byte(jwt))
	if err != nil {
		return fmt.Errorf("cookie_session: encode: %w", err)
	}

	http.SetCookie(rw, &http.Cookie{
		Name:     w.cfg.CookieName,
		Value:    encoded,
		Path:     w.cfg.CookiePath,
		Domain:   w.cfg.CookieDomain,
		MaxAge:   w.cfg.MaxAge,
		Secure:   w.cfg.CookieSecure,
		HttpOnly: true,
		SameSite: w.cfg.CookieSameSite,
	})
	return nil
}

// Clear removes the session cookie by setting MaxAge=-1.
func (w *SessionCookieWriter) Clear(rw http.ResponseWriter) {
	http.SetCookie(rw, &http.Cookie{
		Name:     w.cfg.CookieName,
		Value:    "",
		Path:     w.cfg.CookiePath,
		Domain:   w.cfg.CookieDomain,
		MaxAge:   -1,
		Secure:   w.cfg.CookieSecure,
		HttpOnly: true,
		SameSite: w.cfg.CookieSameSite,
	})
}

// SetSessionCookie writes a signed (optionally encrypted) JWT cookie to the response.
// Returns an error if encoding fails. Callers should handle the error (at minimum log it).
//
// For better performance, use NewSessionCookieWriter to pre-build the SecureCookie
// instance instead of calling this function per-request.
func SetSessionCookie(w http.ResponseWriter, cfg CookieSessionConfig, jwt string) error {
	normalizeCookieSessionConfig(&cfg)

	sc, err := NewSecureCookie(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		slog.Error("cookie_session: failed to create SecureCookie",
			slog.Any("error", err))
		return fmt.Errorf("cookie_session: %w", err)
	}

	encoded, err := sc.Encode(cfg.CookieName, []byte(jwt))
	if err != nil {
		slog.Error("cookie_session: failed to encode cookie",
			slog.Any("error", err))
		return fmt.Errorf("cookie_session: encode: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    encoded,
		Path:     cfg.CookiePath,
		Domain:   cfg.CookieDomain,
		MaxAge:   cfg.MaxAge,
		Secure:   cfg.CookieSecure,
		HttpOnly: true,
		SameSite: cfg.CookieSameSite,
	})
	return nil
}

// ClearSessionCookie removes the session cookie by setting MaxAge=-1.
func ClearSessionCookie(w http.ResponseWriter, cfg CookieSessionConfig) {
	normalizeCookieSessionConfig(&cfg)

	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     cfg.CookiePath,
		Domain:   cfg.CookieDomain,
		MaxAge:   -1,
		Secure:   cfg.CookieSecure,
		HttpOnly: true,
		SameSite: cfg.CookieSameSite,
	})
}
