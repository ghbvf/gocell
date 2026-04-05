package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// RealIP extracts the client's real IP address. It only trusts the
// X-Forwarded-For and X-Real-Ip headers when the request's RemoteAddr is
// from a trusted proxy. If trustedProxies is empty or nil, no proxy is
// trusted and RemoteAddr is always used.
func RealIP(trustedProxies []string) func(http.Handler) http.Handler {
	trusted := make(map[string]bool, len(trustedProxies))
	for _, p := range trustedProxies {
		trusted[p] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r, trusted)
			ctx := ctxkeys.WithRealIP(r.Context(), ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractIP(r *http.Request, trusted map[string]bool) string {
	remoteHost := remoteAddrHost(r.RemoteAddr)

	// Only trust forwarding headers when the direct peer is a trusted proxy.
	if trusted[remoteHost] {
		// Prefer X-Forwarded-For (first entry is the original client).
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.SplitN(xff, ",", 2)
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}

		// Fall back to X-Real-Ip.
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	// Fall back to RemoteAddr (strip port).
	return remoteHost
}

func remoteAddrHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
