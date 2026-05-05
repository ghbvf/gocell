package auth

import (
	"context"
	"crypto/subtle"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// BootstrapCredentials carries the env-driven HTTP Basic Auth credentials
// used to protect the first-admin setup endpoint.
//
// ref: minio/minio internal/auth/credentials.go (length fail-fast at startup)
// ref: keycloak/keycloak KC_BOOTSTRAP_ADMIN_USERNAME/PASSWORD env model
type BootstrapCredentials struct {
	Username []byte
	Password []byte
}

// BootstrapRateLimiter decides whether a request identified by key should be
// allowed. Implementations are injected by the caller so that runtime/auth does
// not import runtime/http/middleware (which imports runtime/auth, creating a
// cycle). Callers wire a concrete middleware.RateLimiter or an
// adapters/ratelimit.TokenBucket which satisfies this interface structurally.
//
// There is no built-in "allow all" implementation — bootstrap routes must
// always carry a real per-IP limiter to defeat brute-force enumeration of
// operator credentials. Tests construct fakes locally if they need to bypass
// rate limiting (see runtime/auth/bootstrap_test.go fakeRateLimiter).
type BootstrapRateLimiter interface {
	Allow(key string) bool
}

// BootstrapAuthFailObserver is invoked after a 401 or 429 response is written.
// The reason string is one of:
//   - "missing_header"    — Basic Auth header absent
//   - "wrong_credentials" — header present but credentials do not match
//   - "rate_limited"      — per-IP token bucket exhausted (429)
//
// Wiring an observer is how callers route bootstrap auth failures to audit logs
// without importing cells/ from runtime/auth.
type BootstrapAuthFailObserver = func(ctx context.Context, reason string)

// NewBootstrapMiddleware constructs the HTTP middleware chain for bootstrap
// authentication. The chain is: RateLimit (per-IP) → Basic Auth header parse →
// constant-time username/password comparison → uniform 401 envelope on any
// mismatch (no field-level oracle).
//
// onAuthFail is an optional observer invoked on every authentication failure
// (after the response is written). The reason string is one of:
// "missing_header", "wrong_credentials", "rate_limited". Callers use this
// hook to write audit log entries without importing cells/. Pass nil to disable.
//
// Wire this middleware around the setup/admin handler to enforce D5 semantics:
// env credentials authenticate the operator; body credentials define the admin
// identity.
func NewBootstrapMiddleware(
	creds BootstrapCredentials,
	limiter BootstrapRateLimiter,
	onAuthFail BootstrapAuthFailObserver,
) func(http.Handler) http.Handler {
	return newBootstrapMiddleware(creds, limiter, onAuthFail)
}

// bootstrapWindowedLimiter extends BootstrapRateLimiter with window metadata for
// Retry-After calculation — mirrors middleware.WindowedRateLimiter.
type bootstrapWindowedLimiter interface {
	BootstrapRateLimiter
	Window() (window time.Duration, limit int)
}

// newBootstrapMiddleware constructs the HTTP middleware chain for bootstrap
// authentication. The chain is: RateLimit (per-IP) → Basic Auth header parse →
// constant-time username/password comparison → uniform 401 envelope on any
// mismatch (no field-level oracle).
//
// All authentication failures share the same response shape and errcode
// (ERR_AUTH_BOOTSTRAP_FAILED) so attackers cannot distinguish "wrong username"
// from "wrong password".
//
// Rate limiting is applied first (before auth parsing) so brute-force is throttled
// regardless of credential presence.
//
// onAuthFail is called after writing the 401 response. nil → no-op.
//
// ref: Go stdlib crypto/subtle.ConstantTimeCompare (timing-safe equality)
func newBootstrapMiddleware(
	creds BootstrapCredentials,
	limiter BootstrapRateLimiter,
	onAuthFail BootstrapAuthFailObserver,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowBootstrapRequest(w, r, limiter, onAuthFail) {
				return
			}
			if reason, ok := authenticateBootstrap(r, creds); !ok {
				writeBootstrapAuthFailed(r.Context(), w)
				if onAuthFail != nil {
					onAuthFail(r.Context(), reason)
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// allowBootstrapRequest enforces the per-IP rate limit and writes the 429
// envelope on rejection. Returns true when the request should proceed.
// On rejection, onAuthFail (if non-nil) is called with reason="rate_limited"
// so audit observers can record the blocked attempt.
func allowBootstrapRequest(
	w http.ResponseWriter,
	r *http.Request,
	limiter BootstrapRateLimiter,
	onAuthFail BootstrapAuthFailObserver,
) bool {
	if limiter.Allow(bootstrapClientIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(bootstrapRetryAfter(limiter)))
	httputil.WriteError(r.Context(), w,
		errcode.New(errcode.KindRateLimited, errcode.ErrRateLimited, "too many requests"))
	if onAuthFail != nil {
		onAuthFail(r.Context(), "rate_limited")
	}
	return false
}

// authenticateBootstrap parses Basic Auth and constant-time-compares the
// supplied credentials against creds. Returns ("", true) on match;
// ("missing_header"|"wrong_credentials", false) on failure. ConstantTimeCompare
// returns 1 only when the slices are equal AND same length; AND-ing the two
// results bitwise keeps the check constant-time across both comparisons.
func authenticateBootstrap(r *http.Request, creds BootstrapCredentials) (string, bool) {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return "missing_header", false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), creds.Username)
	passOK := subtle.ConstantTimeCompare([]byte(pass), creds.Password)
	if userOK&passOK != 1 {
		return "wrong_credentials", false
	}
	return "", true
}

func writeBootstrapAuthFailed(ctx context.Context, w http.ResponseWriter) {
	httputil.WriteError(ctx, w, errcode.New(
		errcode.KindUnauthenticated,
		errcode.ErrAuthBootstrapFailed,
		"bootstrap authentication failed",
	))
}

// bootstrapClientIP extracts the client IP for rate-limit keying.
// Mirrors middleware.clientIP to avoid cross-package dependency.
func bootstrapClientIP(r *http.Request) string {
	if ip, ok := ctxkeys.RealIPFrom(r.Context()); ok && ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// bootstrapRetryAfter computes the Retry-After value in seconds.
// Mirrors middleware.computeRetryAfter to avoid cross-package dependency.
func bootstrapRetryAfter(limiter BootstrapRateLimiter) int {
	if wl, ok := limiter.(bootstrapWindowedLimiter); ok {
		window, limit := wl.Window()
		if limit > 0 && window > 0 {
			secs := window.Seconds() / float64(limit)
			return int(math.Ceil(secs))
		}
	}
	return 1
}
