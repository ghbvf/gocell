package middleware

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// RateLimiter decides whether a request identified by key should be allowed.
type RateLimiter interface {
	// Allow returns true if the request should proceed.
	Allow(key string) bool
}

// WindowedRateLimiter extends RateLimiter with window metadata for dynamic
// Retry-After calculation. Implementations that do not track windows can
// implement only RateLimiter; the middleware will fall back to a default.
type WindowedRateLimiter interface {
	RateLimiter
	// Window returns the rate limit window duration and the maximum number
	// of requests allowed within that window.
	Window() (window time.Duration, limit int)
}

// RateLimit applies per-IP rate limiting using the provided RateLimiter.
// The client IP is obtained from the context (set by the RealIP middleware)
// or falls back to RemoteAddr.
// When the limit is exceeded, a 429 response with a dynamically computed
// Retry-After header is returned.
func RateLimit(limiter RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !limiter.Allow(ip) {
				retryAfter := computeRetryAfter(limiter)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				httputil.WriteError(r.Context(), w, http.StatusTooManyRequests, string(errcode.ErrRateLimited), "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// computeRetryAfter returns the number of seconds a client should wait
// before retrying. If the limiter implements WindowedRateLimiter, the value
// is ceil(window / limit). Otherwise, it defaults to 1 second.
func computeRetryAfter(limiter RateLimiter) int {
	if wl, ok := limiter.(WindowedRateLimiter); ok {
		window, limit := wl.Window()
		if limit > 0 && window > 0 {
			secs := window.Seconds() / float64(limit)
			return int(math.Ceil(secs))
		}
	}
	return 1
}

func clientIP(r *http.Request) string {
	if ip, ok := ctxkeys.RealIPFrom(r.Context()); ok && ip != "" {
		return ip
	}
	// Strip port from RemoteAddr (e.g. "192.168.1.1:54321" → "192.168.1.1")
	// so that connections from the same IP share one rate bucket.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // already host-only or unparseable
	}
	return host
}
