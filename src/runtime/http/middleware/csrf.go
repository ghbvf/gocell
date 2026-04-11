package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// ref: labstack/echo — Sec-Fetch-Site primary defense for modern browsers
// ref: gofiber/fiber — Origin wildcard matching + TrustedOrigins allowlist
// ref: gorilla/csrf — Referer header fallback validation

// CSRFConfig configures the CSRF origin validation middleware.
type CSRFConfig struct {
	// TrustedOrigins: allowed origins (scheme://host[:port]).
	// Supports wildcard subdomains: "https://*.example.com"
	TrustedOrigins []string

	// ExcludedPathPrefixes: URL path prefixes that bypass CSRF checks.
	ExcludedPathPrefixes []string

	// AllowSameSite: whether Sec-Fetch-Site "same-site" is permitted.
	// Default: true.
	AllowSameSite bool

	// AllowMissingOrigin: allow requests without any origin signal
	// (Sec-Fetch-Site, Origin, Referer all absent).
	// Default: true (permissive, for JWT API compatibility).
	// Set false for strict BFF-only mode.
	AllowMissingOrigin bool
}

// DefaultCSRFConfig returns a CSRFConfig with safe defaults.
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		AllowSameSite:      true,
		AllowMissingOrigin: true,
	}
}

// safeMethods are HTTP methods that do not require CSRF validation.
var safeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
	http.MethodTrace:   true,
}

// CSRF returns middleware that validates request origin using Sec-Fetch-Site,
// Origin, and Referer headers. It provides defense-in-depth for APIs that may
// use bearer tokens (JWT) or cookie-based sessions.
func CSRF(cfg CSRFConfig) func(http.Handler) http.Handler {
	// Normalize trusted origins for O(1) lookup (exact matches)
	// and collect wildcard patterns separately.
	exactOrigins := make(map[string]bool)
	var wildcardPatterns []string
	for _, o := range cfg.TrustedOrigins {
		norm := normalizeOrigin(o)
		if strings.Contains(norm, "*.") {
			wildcardPatterns = append(wildcardPatterns, norm)
		} else {
			exactOrigins[norm] = true
		}
	}

	excluded := cfg.ExcludedPathPrefixes
	allowSameSite := cfg.AllowSameSite
	allowMissing := cfg.AllowMissingOrigin

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: Safe methods bypass.
			if safeMethods[r.Method] {
				next.ServeHTTP(w, r)
				return
			}

			// Step 2: Excluded paths bypass.
			if isExcludedPath(r.URL.Path, excluded) {
				next.ServeHTTP(w, r)
				return
			}

			// Step 3: Sec-Fetch-Site validation.
			if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
				switch sfs {
				case "same-origin", "none":
					w.Header().Add("Vary", "Origin")
					next.ServeHTTP(w, r)
					return
				case "same-site":
					if allowSameSite {
						w.Header().Add("Vary", "Origin")
						next.ServeHTTP(w, r)
						return
					}
					rejectCSRF(w, r)
					return
				default: // "cross-site" or unknown
					rejectCSRF(w, r)
					return
				}
			}

			// Step 4: Origin header validation.
			if origin := r.Header.Get("Origin"); origin != "" {
				if matchOrigin(origin, exactOrigins, wildcardPatterns) {
					w.Header().Add("Vary", "Origin")
					next.ServeHTTP(w, r)
					return
				}
				rejectCSRF(w, r)
				return
			}

			// Step 5: Referer header fallback.
			if referer := r.Header.Get("Referer"); referer != "" {
				refOrigin := extractOrigin(referer)
				if refOrigin != "" && matchOrigin(refOrigin, exactOrigins, wildcardPatterns) {
					w.Header().Add("Vary", "Origin")
					next.ServeHTTP(w, r)
					return
				}
				rejectCSRF(w, r)
				return
			}

			// Step 6: No origin signals at all.
			if allowMissing {
				next.ServeHTTP(w, r)
				return
			}
			rejectCSRF(w, r)
		})
	}
}

func rejectCSRF(w http.ResponseWriter, r *http.Request) {
	httputil.WriteError(r.Context(), w, http.StatusForbidden,
		"ERR_CSRF_ORIGIN_DENIED", "cross-origin request denied")
}

// matchOrigin checks if origin matches any exact or wildcard pattern.
func matchOrigin(origin string, exact map[string]bool, wildcards []string) bool {
	norm := normalizeOrigin(origin)
	if exact[norm] {
		return true
	}
	for _, pattern := range wildcards {
		if matchWildcardOrigin(norm, pattern) {
			return true
		}
	}
	return false
}

// matchWildcardOrigin matches "https://sub.example.com" against "https://*.example.com".
func matchWildcardOrigin(origin, pattern string) bool {
	// pattern: "https://*.example.com"
	// Split at "://" to compare schemes.
	pScheme, pHost, pOK := splitOrigin(pattern)
	oScheme, oHost, oOK := splitOrigin(origin)
	if !pOK || !oOK || pScheme != oScheme {
		return false
	}
	// pHost: "*.example.com", oHost: "sub.example.com"
	if !strings.HasPrefix(pHost, "*.") {
		return false
	}
	suffix := pHost[1:] // ".example.com"
	// oHost must end with suffix and have at least one char before it.
	return len(oHost) > len(suffix) && strings.HasSuffix(oHost, suffix)
}

// splitOrigin splits "https://host:port" into (scheme, host:port, ok).
func splitOrigin(origin string) (scheme, host string, ok bool) {
	idx := strings.Index(origin, "://")
	if idx < 0 {
		return "", "", false
	}
	return origin[:idx], origin[idx+3:], true
}

// extractOrigin extracts scheme://host[:port] from a full URL.
func extractOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// normalizeOrigin lowercases and trims trailing slash.
func normalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(o, "/"))
}

// isExcludedPath checks if path starts with any excluded prefix.
func isExcludedPath(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
