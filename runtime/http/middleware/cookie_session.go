package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/securecookie"
)

// ref: alexedwards/scs — LoadAndSave middleware pattern
// ref: gorilla/sessions — CookieStore dual-mode (cookie + header)
// ref: labstack/echo — NewXxx() (error) + MustXxx() dual API pattern
// ref: gofiber/fiber — configDefault() unified normalization

// maxCookieSize is the practical browser cookie size limit.
// Values exceeding this after encoding will be rejected by most browsers.
const maxCookieSize = 4096

// errfCookieSession is the canonical error-wrapping prefix for cookie_session
// operations. Using a const keeps all callers consistent (S1192).
const errfCookieSession = "cookie_session: %w"

// CookieSessionConfig configures the BFF cookie session middleware.
// Clock is not part of this struct; it is passed as a mandatory positional
// parameter to NewCookieSession, NewSessionCookieWriter, and SetSessionCookie.
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
	}
}

// normalizeCookieSessionConfig fills zero-value fields with safe defaults.
// All zero values produce secure cookie attributes.
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

// NewCookieSession creates the cookie session middleware, returning an error
// if the configuration is invalid (e.g., Secret too short).
//
// clk is required and must be a non-nil clock.Clock (clock.Real() in
// production, clockmock.New(...) in tests).
//
// ref: labstack/echo — ToMiddleware() (MiddlewareFunc, error) pattern
func NewCookieSession(clk clock.Clock, cfg CookieSessionConfig) (func(http.Handler) http.Handler, error) {
	clock.MustHaveClock(clk, "middleware.NewCookieSession")
	normalizeCookieSessionConfig(&cfg)

	sc, err := securecookie.New(securecookie.Config{
		HashKey:  cfg.Secret,
		BlockKey: cfg.EncryptKey,
		Clock:    clk,
		MaxAge:   cfg.MaxAge,
	})
	if err != nil {
		return nil, fmt.Errorf(errfCookieSession, err)
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

// NewSessionCookieWriter creates a reusable writer for setting session cookies.
// Pre-builds the SecureCookie instance to avoid per-call reconstruction.
//
// clk is required and must be a non-nil clock.Clock.
func NewSessionCookieWriter(clk clock.Clock, cfg CookieSessionConfig) (*SessionCookieWriter, error) {
	clock.MustHaveClock(clk, "middleware.NewSessionCookieWriter")
	normalizeCookieSessionConfig(&cfg)

	sc, err := securecookie.New(securecookie.Config{
		HashKey:  cfg.Secret,
		BlockKey: cfg.EncryptKey,
		Clock:    clk,
		MaxAge:   cfg.MaxAge,
	})
	if err != nil {
		return nil, fmt.Errorf(errfCookieSession, err)
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
		Secure:   true,
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
		Secure:   true,
		HttpOnly: true,
		SameSite: w.cfg.CookieSameSite,
	})
}

// SetSessionCookie writes a signed (optionally encrypted) JWT cookie to the response.
// Returns an error if encoding fails or cookie exceeds browser size limit.
//
// clk is required and must be a non-nil clock.Clock.
//
// For better performance, use NewSessionCookieWriter to pre-build the SecureCookie
// instance instead of calling this function per-request.
func SetSessionCookie(w http.ResponseWriter, clk clock.Clock, cfg CookieSessionConfig, jwt string) error {
	clock.MustHaveClock(clk, "middleware.SetSessionCookie")
	normalizeCookieSessionConfig(&cfg)

	sc, err := securecookie.New(securecookie.Config{
		HashKey:  cfg.Secret,
		BlockKey: cfg.EncryptKey,
		Clock:    clk,
		MaxAge:   cfg.MaxAge,
	})
	if err != nil {
		slog.Error("cookie_session: failed to create SecureCookie",
			slog.Any("error", err))
		return fmt.Errorf(errfCookieSession, err)
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
		Secure:   true,
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
		Secure:   true,
		HttpOnly: true,
		SameSite: cfg.CookieSameSite,
	})
}
