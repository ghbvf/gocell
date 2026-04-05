package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// RealIP extracts the client's real IP address from X-Forwarded-For or
// X-Real-Ip headers. If neither header is present, it falls back to
// RemoteAddr. The resolved IP is stored in the request context via
// ctxkeys.RealIP.
func RealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		ctx := ctxkeys.WithRealIP(r.Context(), ip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractIP(r *http.Request) string {
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

	// Fall back to RemoteAddr (strip port).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
