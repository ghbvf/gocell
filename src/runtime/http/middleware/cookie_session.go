package middleware

import (
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

// CookieSession returns middleware that reads a JWT from a signed cookie and
// injects it as an Authorization: Bearer header. If the request already has
// an Authorization header, the cookie is ignored (API client mode).
//
// This middleware does NOT set cookies — use SetSessionCookie/ClearSessionCookie
// in login/logout handlers.
func CookieSession(cfg CookieSessionConfig) func(http.Handler) http.Handler {
	name := cfg.CookieName
	if name == "" {
		name = "session"
	}

	// Build SecureCookie instance at construction time.
	sc, err := NewSecureCookie(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		// Fail-fast: configuration error should surface immediately.
		panic("cookie_session: invalid config: " + err.Error())
	}
	maxAge := cfg.MaxAge
	if maxAge > 0 {
		sc = sc.WithMaxAge(maxAge)
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
			cookie, err := r.Cookie(name)
			if err != nil || cookie.Value == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Decode and verify cookie.
			jwt, err := sc.Decode(name, cookie.Value)
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

// SetSessionCookie writes a signed (optionally encrypted) JWT cookie to the response.
// Called by login/refresh handlers after issuing tokens.
func SetSessionCookie(w http.ResponseWriter, cfg CookieSessionConfig, jwt string) {
	name := cfg.CookieName
	if name == "" {
		name = "session"
	}

	sc, err := NewSecureCookie(cfg.Secret, cfg.EncryptKey)
	if err != nil {
		// Should not happen if config was validated at startup.
		return
	}

	encoded, err := sc.Encode(name, []byte(jwt))
	if err != nil {
		return
	}

	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = 900
	}

	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    encoded,
		Path:     cookiePath(cfg.CookiePath),
		Domain:   cfg.CookieDomain,
		MaxAge:   maxAge,
		Secure:   cfg.CookieSecure,
		HttpOnly: true, // Always HttpOnly — never accessible from JS
		SameSite: cookieSameSite(cfg.CookieSameSite),
	})
}

// ClearSessionCookie removes the session cookie by setting MaxAge=-1.
// Called by logout handler.
func ClearSessionCookie(w http.ResponseWriter, cfg CookieSessionConfig) {
	name := cfg.CookieName
	if name == "" {
		name = "session"
	}

	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     cookiePath(cfg.CookiePath),
		Domain:   cfg.CookieDomain,
		MaxAge:   -1,
		Secure:   cfg.CookieSecure,
		HttpOnly: true,
		SameSite: cookieSameSite(cfg.CookieSameSite),
	})
}

func cookiePath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func cookieSameSite(s http.SameSite) http.SameSite {
	if s == 0 {
		return http.SameSiteStrictMode
	}
	return s
}
