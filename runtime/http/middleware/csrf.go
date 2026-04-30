package middleware

import (
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// ref: labstack/echo — Sec-Fetch-Site primary defense for modern browsers
// ref: gofiber/fiber — Origin wildcard matching + TrustedOrigins allowlist
// ref: gorilla/csrf — Referer header fallback validation

// CSRFConfig configures the CSRF origin validation middleware.
type CSRFConfig struct {
	// TrustedOrigins: allowed origins (scheme://host[:port]).
	// Supports wildcard subdomains: "https://*.example.com"
	//
	// Note: wildcards match ALL subdomain depths — "https://*.example.com"
	// matches both "https://sub.example.com" and "https://a.b.c.example.com".
	// For tighter control, list specific subdomains explicitly.
	TrustedOrigins []string

	// ExcludedPathPrefixes: URL path prefixes that bypass CSRF checks.
	// Paths are normalized with path.Clean before matching to prevent
	// traversal bypasses (e.g., /api/webhooks/../secret).
	ExcludedPathPrefixes []string

	// AllowSameSite controls behavior for Sec-Fetch-Site: same-site requests.
	// When true, same-site requests fall through to Origin/Referer validation
	// (not blindly allowed — still requires TrustedOrigins match).
	// When false, same-site requests are immediately rejected.
	// Default: false (secure default — same-site subdomain attacks blocked).
	AllowSameSite bool

	// AllowMissingOrigin controls behavior when no origin signal
	// (Sec-Fetch-Site, Origin, Referer) is present.
	// Default: false (fail-closed — all requests must carry origin info).
	// Set true only for API-only endpoints where non-browser clients
	// (cURL, server-to-server) are expected.
	AllowMissingOrigin bool
}

// DefaultCSRFConfig returns a CSRFConfig with secure defaults.
// Both AllowSameSite and AllowMissingOrigin default to false (fail-closed).
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		AllowSameSite:      false,
		AllowMissingOrigin: false,
	}
}

// safeMethods are HTTP methods that do not require CSRF validation.
var safeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
	http.MethodTrace:   true,
}

// csrfState holds the normalized configuration for a CSRF middleware instance.
type csrfState struct {
	exactOrigins     map[string]bool
	wildcardPatterns []string
	excluded         []string
	allowSameSite    bool
	allowMissing     bool
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
	st := csrfState{
		exactOrigins:     exactOrigins,
		wildcardPatterns: wildcardPatterns,
		excluded:         cfg.ExcludedPathPrefixes,
		allowSameSite:    cfg.AllowSameSite,
		allowMissing:     cfg.AllowMissingOrigin,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: Safe methods bypass.
			if safeMethods[r.Method] {
				next.ServeHTTP(w, r)
				return
			}
			// Step 2: Excluded paths bypass.
			// Normalize path to prevent traversal (e.g., /a/../b → /b).
			if isExcludedPath(path.Clean(r.URL.Path), st.excluded) {
				next.ServeHTTP(w, r)
				return
			}
			// Steps 3-6: origin signal validation.
			if !st.checkOriginSignals(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// checkOriginSignals validates the Sec-Fetch-Site, Origin, and Referer headers
// in sequence. Returns true if the request may proceed, false if it was
// rejected (rejection response already written to w).
func (st *csrfState) checkOriginSignals(w http.ResponseWriter, r *http.Request) bool {
	// Step 3: Sec-Fetch-Site validation.
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		return st.checkSecFetchSite(w, r, sfs)
	}
	// Step 4: Origin header validation.
	if origin := r.Header.Get("Origin"); origin != "" {
		return st.checkOriginHeader(w, r, origin)
	}
	// Step 5: Referer header fallback.
	if referer := r.Header.Get("Referer"); referer != "" {
		return st.checkRefererHeader(w, r, referer)
	}
	// Step 6: No origin signals at all.
	if st.allowMissing {
		return true
	}
	rejectCSRF(w, r, "no origin signal present")
	return false
}

// checkSecFetchSite handles Sec-Fetch-Site validation (step 3).
// Returns true if the request may proceed or must fall through to further
// validation; false if it was rejected.
func (st *csrfState) checkSecFetchSite(w http.ResponseWriter, r *http.Request, sfs string) bool {
	switch sfs {
	case "same-origin", "none":
		w.Header().Add("Vary", "Origin")
		return true
	case "same-site":
		if !st.allowSameSite {
			rejectCSRF(w, r, "same-site not allowed")
			return false
		}
		// AllowSameSite=true: fall through to Origin/Referer
		// validation — do NOT blindly allow. A malicious
		// subdomain could send same-site requests.
		return st.checkOriginSignalsNoSFS(w, r)
	default: // "cross-site" or unknown
		rejectCSRF(w, r, "cross-site or unknown Sec-Fetch-Site: "+sfs)
		return false
	}
}

// checkOriginSignalsNoSFS checks Origin and Referer when Sec-Fetch-Site
// already passed but did not grant access (same-site with AllowSameSite=true).
func (st *csrfState) checkOriginSignalsNoSFS(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return st.checkOriginHeader(w, r, origin)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return st.checkRefererHeader(w, r, referer)
	}
	if st.allowMissing {
		return true
	}
	rejectCSRF(w, r, "no origin signal present")
	return false
}

// checkOriginHeader validates the Origin header (step 4).
func (st *csrfState) checkOriginHeader(w http.ResponseWriter, r *http.Request, origin string) bool {
	if origin == "null" {
		// Origin: null is sent by sandboxed iframes, data: URLs,
		// and redirects — treat as untrusted.
		rejectCSRF(w, r, "null origin")
		return false
	}
	if matchOrigin(origin, st.exactOrigins, st.wildcardPatterns) {
		w.Header().Add("Vary", "Origin")
		return true
	}
	rejectCSRF(w, r, "origin not trusted: "+origin)
	return false
}

// checkRefererHeader validates the Referer header (step 5).
func (st *csrfState) checkRefererHeader(w http.ResponseWriter, r *http.Request, referer string) bool {
	refOrigin := extractOrigin(referer)
	if refOrigin != "" && matchOrigin(refOrigin, st.exactOrigins, st.wildcardPatterns) {
		w.Header().Add("Vary", "Origin")
		return true
	}
	rejectCSRF(w, r, "referer not trusted: "+referer)
	return false
}

func rejectCSRF(w http.ResponseWriter, r *http.Request, reason string) {
	// Structured log for ops visibility — distinguish misconfig vs real attack.
	attrs := []any{
		slog.String("reason", reason),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		attrs = append(attrs, slog.String("origin", origin))
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		attrs = append(attrs, slog.String("sec_fetch_site", sfs))
	}
	if reqID, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
		attrs = append(attrs, slog.String("request_id", reqID))
	}
	slog.Warn("csrf: request rejected", attrs...) //nolint:gosec // G706: structured slog attrs, not string concatenation

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
	pScheme, pHost, pOK := splitOrigin(pattern)
	oScheme, oHost, oOK := splitOrigin(origin)
	if !pOK || !oOK || pScheme != oScheme {
		return false
	}
	if !strings.HasPrefix(pHost, "*.") {
		return false
	}
	suffix := pHost[1:] // ".example.com"
	return len(oHost) > len(suffix) && strings.HasSuffix(oHost, suffix)
}

// splitOrigin splits "https://host:port" into (scheme, host:port, ok).
func splitOrigin(origin string) (scheme, host string, ok bool) {
	before, after, ok := strings.Cut(origin, "://")
	if !ok {
		return "", "", false
	}
	return before, after, true
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
func isExcludedPath(cleanedPath string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(cleanedPath, p) {
			return true
		}
	}
	return false
}
