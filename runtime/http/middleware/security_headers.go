package middleware

import (
	"fmt"
	"net/http"
)

const defaultHSTSMaxAge = 63072000 // 2 years in seconds

// SecurityHeadersOption configures the SecurityHeadersWithOptions middleware.
//
// ref: unrolled/secure — STSIncludeSubdomains / STSPreload (affirmative opt-in)
// Adopted: affirmative logic consistent with GoCell's RequestIDOption / TracingOption pattern.
// Deviated from Echo's negative HSTSExcludeSubdomains to keep API intuitive.
type SecurityHeadersOption func(*securityHeadersConfig)

type securityHeadersConfig struct {
	maxAge            int
	includeSubDomains bool
	preload           bool
}

// WithHSTSMaxAge overrides the default HSTS max-age (63072000 seconds / 2 years).
// Zero is valid per RFC 6797 (instructs browsers to remove HSTS). Negative
// values are clamped to zero.
func WithHSTSMaxAge(seconds int) SecurityHeadersOption {
	return func(c *securityHeadersConfig) {
		if seconds < 0 {
			seconds = 0
		}
		c.maxAge = seconds
	}
}

// WithHSTSIncludeSubDomains appends the includeSubDomains directive to the
// Strict-Transport-Security header. Off by default — callers must explicitly
// opt in after verifying that all subdomains support HTTPS.
func WithHSTSIncludeSubDomains() SecurityHeadersOption {
	return func(c *securityHeadersConfig) { c.includeSubDomains = true }
}

// WithHSTSPreload appends the preload directive to the Strict-Transport-Security
// header. Off by default — callers must explicitly opt in after confirming
// eligibility at hstspreload.org.
func WithHSTSPreload() SecurityHeadersOption {
	return func(c *securityHeadersConfig) { c.preload = true }
}

// SecurityHeadersWithOptions creates a middleware that sets security-related
// response headers. The HSTS header is always emitted with at least max-age;
// includeSubDomains and preload require explicit opt-in via options.
//
// Headers set:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Strict-Transport-Security: max-age=<N>[; includeSubDomains][; preload]
func SecurityHeadersWithOptions(opts ...SecurityHeadersOption) func(http.Handler) http.Handler {
	cfg := securityHeadersConfig{maxAge: defaultHSTSMaxAge}
	for _, o := range opts {
		o(&cfg)
	}

	// Build the HSTS value once at construction time.
	stsValue := fmt.Sprintf("max-age=%d", cfg.maxAge)
	if cfg.includeSubDomains {
		stsValue += "; includeSubDomains"
	}
	if cfg.preload {
		stsValue += "; preload"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Strict-Transport-Security", stsValue)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets security-related response headers on every request.
// This is a convenience wrapper around SecurityHeadersWithOptions with default
// configuration (HSTS max-age=63072000, no includeSubDomains, no preload).
func SecurityHeaders(next http.Handler) http.Handler {
	return SecurityHeadersWithOptions()(next)
}
