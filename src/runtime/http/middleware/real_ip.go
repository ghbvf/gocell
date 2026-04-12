package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// proxyChecker determines whether a given IP is a trusted proxy.
// Supports both exact IP addresses and CIDR notation.
//
// ref: gin-gonic/gin — prepareTrustedCIDRs() for CIDR parsing
// ref: labstack/echo — TrustIPRange for CIDR-based trust
type proxyChecker struct {
	exact map[string]bool
	cidrs []*net.IPNet
}

func newProxyChecker(proxies []string) *proxyChecker {
	pc := &proxyChecker{exact: make(map[string]bool, len(proxies))}
	for _, p := range proxies {
		if _, cidr, err := net.ParseCIDR(p); err == nil {
			pc.cidrs = append(pc.cidrs, cidr)
		} else if parsed := net.ParseIP(p); parsed != nil {
			// Store canonical form so "::1" and "0:0:0:0:0:0:0:1" match.
			pc.exact[parsed.String()] = true
		} else {
			// Not a valid IP or CIDR — store as-is (will never match a real IP).
			pc.exact[p] = true
		}
	}
	return pc
}

func (pc *proxyChecker) empty() bool {
	return len(pc.exact) == 0 && len(pc.cidrs) == 0
}

func (pc *proxyChecker) isTrusted(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		// Not a valid IP — check raw string (handles edge case of
		// invalid entries stored via newProxyChecker fallback path).
		return pc.exact[ip]
	}
	// Canonical form lookup matches how newProxyChecker stores IPs.
	if pc.exact[parsed.String()] {
		return true
	}
	for _, cidr := range pc.cidrs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// RealIP extracts the client's real IP address. It only trusts the
// X-Forwarded-For and X-Real-Ip headers when the request's RemoteAddr is
// from a trusted proxy. If trustedProxies is empty or nil, no proxy is
// trusted and RemoteAddr is always used.
//
// When proxies are trusted, X-Forwarded-For is scanned right-to-left to
// find the first IP that is NOT a trusted proxy. This prevents client-side
// header spoofing attacks.
//
// ref: labstack/echo — ExtractIPFromXFFHeader right-to-left scanning
// ref: gin-gonic/gin — TrustedProxies CIDR list + reverse XFF scan
func RealIP(trustedProxies []string) func(http.Handler) http.Handler {
	checker := newProxyChecker(trustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r, checker)
			ctx := ctxkeys.WithRealIP(r.Context(), ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractIP(r *http.Request, checker *proxyChecker) string {
	remoteHost := remoteAddrHost(r.RemoteAddr)

	if checker.empty() {
		return remoteHost
	}

	if !checker.isTrusted(remoteHost) {
		return remoteHost
	}

	// Prefer X-Forwarded-For, scanning right-to-left.
	// The rightmost untrusted IP is the client.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if ip == "" {
				continue
			}
			if !checker.isTrusted(ip) {
				return ip
			}
		}
		// All IPs in XFF are trusted — return leftmost as the client.
		if first := strings.TrimSpace(parts[0]); first != "" {
			return first
		}
	}

	// Fall back to X-Real-Ip.
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}

	return remoteHost
}

func remoteAddrHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
