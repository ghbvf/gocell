package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/pkg/securecookie"
)

// ref: alexedwards/scs — LoadAndSave middleware pattern
// ref: gorilla/sessions — CookieStore dual-mode (cookie + header)
// ref: labstack/echo — NewXxx() (error) + MustXxx() dual API pattern
// ref: gofiber/fiber — configDefault() unified normalization

// maxCookieSize is the practical browser cookie size limit.
// Values exceeding this after encoding will be rejected by most browsers.
const maxCookieSize = 4096

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

	// Insecure disables the Secure flag on cookies. Only use for local
	// development over plain HTTP (http://localhost).
	// Default: false (cookies are always Secure).
	Insecure bool

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
		CookieSameSite: http.SameSiteStrictMode,
		MaxAge:         900,
		// Insecure defaults to false → Secure=true
	}
}

// normalizeCookieSessionConfig fills zero-value fields with safe defaults.
// All zero values produce secure behavior (Secure=true via !Insecure).
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
}

// cookieSecure returns true unless Insecure is explicitly set.
func (cfg *CookieSessionConfig) cookieSecure() bool {
	return !cfg.Insecure
}

// NewCookieSession creates the cookie session middleware, returning an error
// if the configuration is invalid (e.g., Secret too short).
//
// ref: labstack/echo — ToMiddleware() (MiddlewareFunc, error) pattern
func NewCookieSession(cfg CookieSessionConfig) (func(http.Handler) http.Handler, error) {
	normalizeCookieSessionConfig(&cfg)

	sc, err := securecookie.New(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		return nil, fmt.Errorf("cookie_session: %w", err)
	}
	if cfg.MaxAge > 0 {
		sc = sc.WithMaxAge(cfg.MaxAge)
	}

	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If a Bearer token is already present, skip cookie processing.
			// Non-Bearer Authorization schemes (e.g., Basic) do NOT suppress
			// cookie injection — they are not JWT-compatible.
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
				// Invalid/expired cookie — let AuthMiddleware handle 401.
				next.ServeHTTP(w, r)
				return
			}

			// Inject JWT as Authorization header in-place for downstream
			// AuthMiddleware. This avoids r.Clone() allocation per request.
			r.Header.Set("Authorization", "Bearer "+string(jwt))
			next.ServeHTTP(w, r)
		})
	}
	return mw, nil
}

// MustCookieSession is like NewCookieSession but panics on error.
// Use at init time when configuration errors are programming mistakes.
func MustCookieSession(cfg CookieSessionConfig) func(http.Handler) http.Handler {
	mw, err := NewCookieSession(cfg)
	if err != nil {
		panic("cookie_session: " + err.Error())
	}
	return mw
}

// NewSessionCookieWriter creates a reusable writer for setting session cookies.
// Pre-builds the SecureCookie instance to avoid per-call reconstruction.
func NewSessionCookieWriter(cfg CookieSessionConfig) (*SessionCookieWriter, error) {
	normalizeCookieSessionConfig(&cfg)

	sc, err := securecookie.New(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		return nil, fmt.Errorf("cookie_session: %w", err)
	}

	return &SessionCookieWriter{sc: sc, cfg: cfg}, nil
}

// SessionCookieWriter writes and clears session cookies using a pre-built
// SecureCookie instance for consistent performance.
type SessionCookieWriter struct {
	sc  *securecookie.SecureCookie
	cfg CookieSessionConfig
}

// Set writes a signed (optionally encrypted) JWT cookie to the response.
// Returns an error if the encoded cookie exceeds the browser size limit (4096 bytes).
func (w *SessionCookieWriter) Set(rw http.ResponseWriter, jwt string) error {
	encoded, err := w.sc.Encode(w.cfg.CookieName, []byte(jwt))
	if err != nil {
		return fmt.Errorf("cookie_session: encode: %w", err)
	}

	if len(encoded) > maxCookieSize {
		return fmt.Errorf("cookie_session: encoded cookie size %d exceeds browser limit %d", len(encoded), maxCookieSize)
	}

	http.SetCookie(rw, &http.Cookie{
		Name:     w.cfg.CookieName,
		Value:    encoded,
		Path:     w.cfg.CookiePath,
		Domain:   w.cfg.CookieDomain,
		MaxAge:   w.cfg.MaxAge,
		Secure:   w.cfg.cookieSecure(),
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
		Secure:   w.cfg.cookieSecure(),
		HttpOnly: true,
		SameSite: w.cfg.CookieSameSite,
	})
}

// SetSessionCookie writes a signed (optionally encrypted) JWT cookie to the response.
// Returns an error if encoding fails or cookie exceeds browser size limit.
//
// For better performance, use NewSessionCookieWriter to pre-build the SecureCookie
// instance instead of calling this function per-request.
func SetSessionCookie(w http.ResponseWriter, cfg CookieSessionConfig, jwt string) error {
	normalizeCookieSessionConfig(&cfg)

	sc, err := securecookie.New(cfg.Secret, cfg.EncryptKey)
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

	if len(encoded) > maxCookieSize {
		return fmt.Errorf("cookie_session: encoded cookie size %d exceeds browser limit %d", len(encoded), maxCookieSize)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    encoded,
		Path:     cfg.CookiePath,
		Domain:   cfg.CookieDomain,
		MaxAge:   cfg.MaxAge,
		Secure:   cfg.cookieSecure(),
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
		Secure:   cfg.cookieSecure(),
		HttpOnly: true,
		SameSite: cfg.CookieSameSite,
	})
}
