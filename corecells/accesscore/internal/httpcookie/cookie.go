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
// commits the status, so [Middleware] wraps the writer via
// runtime/http/cellmw.WrapBeforeCommit (an httpsnoop-backed, optional-interface
// preserving wrapper — httpsnoop lives in runtime/ because corecells/ may not import
// it) and emits the cookie at commit time, driven by a ctx directive the adapter
// records via [SetRefresh] / [ClearRefresh] — a response-side ctx directive
// analogous to how request-side params reach the handler.
//
// # Host-binding
//
// The cookie name carries the browser-enforced __Host- prefix (see [CookieName]):
// a long-lived refresh credential read cookie-first must be bound to the exact
// host so a sibling subdomain cannot set or override it (cookie tossing /
// session fixation).
//
// Design, security model (XSS via HttpOnly, CSRF via SameSite=Strict, host
// fixation via __Host-, CORS deferred) and AI-robust grading: ADR
// docs/architecture/202606060000-1278-adr-refresh-cookie-delivery.md.
package httpcookie

import (
	"context"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/runtime/http/cellmw"
)

const (
	// CookieName is the httpOnly refresh-token cookie name (BR-005). The
	// browser-enforced __Host- prefix binds the cookie to the exact host: the
	// browser only accepts a __Host- cookie that is Secure, has no Domain
	// attribute, and uses Path=/, and a sibling subdomain cannot set a __Host-
	// cookie that reaches another host. For a long-lived refresh credential read
	// cookie-first ([IncomingRefresh]), this closes the cookie tossing / session
	// fixation vector that a plain host-scoped name leaves open.
	CookieName = "__Host-gocell_rt"

	// CookiePath is "/" — mandated by the __Host- prefix on [CookieName] (the
	// browser rejects a __Host- cookie with any other Path). It deliberately is
	// NOT narrowed to the sessions subtree: host-binding requires Path=/, and the
	// exposure that a broad path adds is already covered by HttpOnly (JS cannot
	// read it) + Secure (HTTPS-only) + SameSite=Strict (not attached cross-site).
	CookiePath = "/"

	// cookieClearMaxAge is the MaxAge value that instructs the browser to delete
	// the cookie immediately. The stdlib renders MaxAge<0 as the wire string
	// "Max-Age=0" (delete-now directive per RFC 6265 §5.2.2).
	cookieClearMaxAge = -1

	// maxIncomingCookieLen caps the length of the inbound refresh cookie value
	// that is forwarded into ctx. Values longer than this are silently ignored
	// (defense-in-depth: the refresh-token opaque format is bounded, and
	// oversized values cannot match any issued token).
	maxIncomingCookieLen = 4096
)

// newRefreshCookie is the SOLE constructor for the refresh-token cookie. Its
// attributes are the whole point of #1278 and are hard-coded here (no Secure dev
// toggle: localhost is a secure context, so Secure cookies are delivered in
// local dev):
//
//   - HttpOnly / Secure / SameSite=Strict — XSS / transport / CSRF defense.
//   - The __Host- prefix on [CookieName] is satisfied structurally: Secure=true,
//     no Domain field, Path="/" — the three constraints the browser requires.
//
// archtest REFRESH-COOKIE-SECURE-ATTRS-01 form-locks every one of these (the
// three security attributes plus the __Host- name / Path=/ / absent Domain);
// any weakening fails CI.
//
// A clear directive is expressed as value=="" with maxAge==cookieClearMaxAge,
// which renders on the wire as Max-Age=0 (delete now).
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

// IncomingRefresh returns the refresh token carried by the inbound refresh
// cookie, or "" if absent. The refresh adapter selects cookie-first,
// body-fallback (issue requirement #2).
func IncomingRefresh(ctx context.Context) string {
	v, _ := ctx.Value(incomingCtxKey{}).(string)
	return v
}

// Middleware wraps a handler so that:
//
//   - the inbound refresh cookie is read into ctx for [IncomingRefresh], and
//   - a [SetRefresh] / [ClearRefresh] directive recorded by the inner handler is
//     emitted as a Set-Cookie header on a 2xx response.
//
// ttl is the refresh-token lifetime, used as the cookie Max-Age on set
// (the refresh token is reissued with a fresh TTL on every rotation, so a static
// Max-Age stays aligned with the token's hard expiry).
//
// The response writer is wrapped via [cellmw.WrapBeforeCommit], which uses
// httpsnoop to preserve the underlying writer's optional interfaces
// (Flusher / Hijacker / ReaderFrom / Pusher). The httpsnoop plumbing lives in
// runtime/ because corecells/ may not import httpsnoop (cells-isolation depguard);
// this package supplies only the cookie-emission hook below. The hook runs
// before the status commits and only on a 2xx response: a 4xx/5xx must neither
// mint nor clear the refresh cookie (a refresh-reuse 401, for example, leaves
// the stale cookie untouched — it is already useless after the cascade revoke,
// and the 401 request itself is untrusted).
func Middleware(ttl time.Duration) func(http.Handler) http.Handler {
	maxAge := int(ttl.Seconds())
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := &directive{}
			ctx := context.WithValue(r.Context(), directiveCtxKey{}, d)
			if c, err := r.Cookie(CookieName); err == nil && len(c.Value) <= maxIncomingCookieLen {
				ctx = context.WithValue(ctx, incomingCtxKey{}, c.Value)
			}
			wrapped := cellmw.WrapBeforeCommit(w, func(status int) {
				if status < 200 || status >= 300 {
					return
				}
				switch {
				case d.set:
					http.SetCookie(w, newRefreshCookie(d.value, maxAge))
				case d.clear:
					http.SetCookie(w, newRefreshCookie("", cookieClearMaxAge))
				}
			})
			next.ServeHTTP(wrapped, r.WithContext(ctx))
		})
	}
}
