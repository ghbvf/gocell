package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// RateLimiter decides whether a request identified by key should be allowed.
type RateLimiter interface {
	// Allow returns true if the request should proceed.
	Allow(key string) bool
}

// RateLimit applies per-IP rate limiting using the provided RateLimiter.
// The client IP is obtained from the context (set by the RealIP middleware)
// or falls back to RemoteAddr.
// When the limit is exceeded, a 429 response with Retry-After header is returned.
func RateLimit(limiter RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !limiter.Allow(ip) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    "ERR_RATE_LIMITED",
						"message": "too many requests",
					},
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if ip, ok := ctxkeys.RealIPFrom(r.Context()); ok && ip != "" {
		return ip
	}
	return r.RemoteAddr
}
