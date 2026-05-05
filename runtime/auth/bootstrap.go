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

// bootstrapRateLimiter decides whether a request identified by key should be allowed.
// This is a local interface to avoid an import cycle: runtime/http/middleware imports
// runtime/auth (via access_log.go), so runtime/auth must not import runtime/http/middleware.
// Callers wire a concrete middleware.RateLimiter (or adapters/ratelimit.TokenBucket) which
// satisfies this interface structurally.
type bootstrapRateLimiter interface {
	Allow(key string) bool
}

// bootstrapWindowedLimiter extends bootstrapRateLimiter with window metadata for
// Retry-After calculation — mirrors middleware.WindowedRateLimiter.
type bootstrapWindowedLimiter interface {
	bootstrapRateLimiter
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
// ref: Go stdlib crypto/subtle.ConstantTimeCompare (timing-safe equality)
func newBootstrapMiddleware(creds BootstrapCredentials, limiter bootstrapRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := bootstrapClientIP(r)
			if !limiter.Allow(ip) {
				retryAfter := bootstrapRetryAfter(limiter)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				httputil.WriteError(r.Context(), w,
					errcode.New(errcode.KindRateLimited, errcode.ErrRateLimited, "too many requests"))
				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok {
				writeBootstrapAuthFailed(r.Context(), w)
				return
			}
			// ConstantTimeCompare returns 1 only if both slices are equal AND same
			// length. Combine via & (bitwise AND on int) so the boolean check is
			// constant-time across both comparisons — no early return on the first
			// mismatch.
			userOK := subtle.ConstantTimeCompare([]byte(user), creds.Username)
			passOK := subtle.ConstantTimeCompare([]byte(pass), creds.Password)
			if userOK&passOK != 1 {
				writeBootstrapAuthFailed(r.Context(), w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
func bootstrapRetryAfter(limiter bootstrapRateLimiter) int {
	if wl, ok := limiter.(bootstrapWindowedLimiter); ok {
		window, limit := wl.Window()
		if limit > 0 && window > 0 {
			secs := window.Seconds() / float64(limit)
			return int(math.Ceil(secs))
		}
	}
	return 1
}
