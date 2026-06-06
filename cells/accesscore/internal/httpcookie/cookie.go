// Package httpcookie delivers the accesscore refresh token as an httpOnly
// cookie on the session endpoints (login / refresh / logout) and reads it back
// on refresh. It is the accesscore-internal mechanism for BR-005 (#1278):
// gocell-web stores access/refresh tokens only in memory (XSS defense), so it
// needs an httpOnly cookie — the only browser store that survives a cold start
// yet is unreadable by JavaScript — to silently re-authenticate.
//
// # Why a response-writer wrapper
//
// The codegen HTTP handler serializes a typed response envelope via
// visitXxxResponse → w.WriteHeader; the slice adapter never sees the
// http.ResponseWriter. A Set-Cookie header must be written BEFORE WriteHeader
// commits the status, so [Middleware] wraps the writer and emits the cookie at
// WriteHeader time, driven by a ctx directive the adapter records via
// [SetRefresh] / [ClearRefresh]. This mirrors the existing injectLoginTenant
// pattern (request-side ctx injection) on the response side.
//
// Design, security model (XSS via HttpOnly, CSRF via SameSite=Strict, CORS
// deferred) and AI-robust grading: ADR
// docs/architecture/202606060000-1278-adr-refresh-cookie-delivery.md.
package httpcookie

import (
	"context"
	"net/http"
)

const (
	// CookieName is the httpOnly refresh-token cookie name (BR-005).
	CookieName = "gocell_rt"

	// CookiePath narrows the cookie to the sessions subtree so it is only ever
	// attached to /sessions/refresh + logout, reducing its exposure surface.
	CookiePath = "/api/v1/access/sessions"
)

// newRefreshCookie is the SOLE constructor for the refresh-token cookie. The
// three security attributes — HttpOnly, Secure, SameSite=Strict — are the whole
// point of #1278 and are hard-coded here (no Secure dev toggle: localhost is a
// secure context, so Secure cookies are delivered in local dev). archtest
// REFRESH-COOKIE-SECURE-ATTRS-01 form-locks these attributes; any weakening
// fails CI.
//
// A clear directive is expressed as value=="" with maxAge<0, which renders on
// the wire as Max-Age=0 (delete now).
func newRefreshCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     CookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// directive carries the adapter's intent (set or clear the refresh cookie) from
// the slice handler to the response-writer wrapper. A pointer to it is stored in
// ctx by [Middleware]; the adapter mutates the pointee.
type directive struct {
	set   bool
	value string
	clear bool
}

type directiveCtxKey struct{}

type incomingCtxKey struct{}

func directiveFrom(ctx context.Context) *directive {
	d, _ := ctx.Value(directiveCtxKey{}).(*directive)
	return d
}

// SetRefresh records intent to emit a Set-Cookie carrying token on the (2xx)
// response. It is a no-op when the request was not wrapped by [Middleware]
// (e.g. an adapter unit test that bypasses the cookie layer).
func SetRefresh(ctx context.Context, token string) {
	if d := directiveFrom(ctx); d != nil {
		d.set = true
		d.value = token
		d.clear = false
	}
}

// ClearRefresh records intent to clear the refresh cookie on the (2xx) response.
// No-op when the request was not wrapped by [Middleware].
func ClearRefresh(ctx context.Context) {
	if d := directiveFrom(ctx); d != nil {
		d.clear = true
		d.set = false
		d.value = ""
	}
}

// IncomingRefresh returns the refresh token carried by the inbound gocell_rt
// cookie, or "" if absent. The refresh adapter selects cookie-first,
// body-fallback (issue requirement #2).
func IncomingRefresh(ctx context.Context) string {
	v, _ := ctx.Value(incomingCtxKey{}).(string)
	return v
}

// Middleware wraps a handler so that:
//
//   - the inbound gocell_rt cookie is read into ctx for [IncomingRefresh], and
//   - a [SetRefresh] / [ClearRefresh] directive recorded by the inner handler is
//     emitted as a Set-Cookie header on a 2xx response.
//
// maxAge is the refresh-token TTL in seconds, used as the cookie Max-Age on set
// (the refresh token is reissued with a fresh TTL on every rotation, so a static
// Max-Age stays aligned with the token's hard expiry).
func Middleware(maxAge int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := &directive{}
			ctx := context.WithValue(r.Context(), directiveCtxKey{}, d)
			if c, err := r.Cookie(CookieName); err == nil {
				ctx = context.WithValue(ctx, incomingCtxKey{}, c.Value)
			}
			cw := &cookieResponseWriter{ResponseWriter: w, d: d, maxAge: maxAge}
			next.ServeHTTP(cw, r.WithContext(ctx))
		})
	}
}

// cookieResponseWriter intercepts WriteHeader (and the implicit WriteHeader on
// first Write) to emit the Set-Cookie header before the status is committed.
type cookieResponseWriter struct {
	http.ResponseWriter
	d      *directive
	maxAge int
	wrote  bool
}

func (cw *cookieResponseWriter) WriteHeader(status int) {
	cw.emitCookie(status)
	cw.ResponseWriter.WriteHeader(status)
}

func (cw *cookieResponseWriter) Write(b []byte) (int, error) {
	cw.emitCookie(http.StatusOK)
	return cw.ResponseWriter.Write(b)
}

// emitCookie writes the Set-Cookie header exactly once, before the underlying
// status commits, and only on a 2xx response. A 4xx/5xx must neither mint nor
// clear the refresh cookie: a refresh-reuse 401, for example, leaves the stale
// cookie untouched (it is already useless after the cascade revoke), and the
// 401 request itself is untrusted.
func (cw *cookieResponseWriter) emitCookie(status int) {
	if cw.wrote {
		return
	}
	cw.wrote = true
	if status < 200 || status >= 300 {
		return
	}
	switch {
	case cw.d.set:
		http.SetCookie(cw.ResponseWriter, newRefreshCookie(cw.d.value, cw.maxAge))
	case cw.d.clear:
		http.SetCookie(cw.ResponseWriter, newRefreshCookie("", -1))
	}
}
