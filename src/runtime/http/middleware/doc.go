// Package middleware provides chi-compatible HTTP middleware for GoCell applications.
//
// Each middleware follows the func(http.Handler) http.Handler signature and can
// be composed via standard chaining.
//
// Available middleware:
//   - RequestID: injects a unique X-Request-ID header
//   - RealIP: extracts the client IP from X-Forwarded-For / X-Real-IP
//   - Recovery: recovers from panics and returns 500
//   - AccessLog: structured request/response logging via slog
//   - SecurityHeaders: sets secure default HTTP headers
//   - BodyLimit: enforces a maximum request body size
//   - RateLimit: token-bucket rate limiting per client IP
//   - CSRF: validates request origin via Sec-Fetch-Site, Origin, and Referer headers
//   - CookieSession: BFF cookie session with signed JWT encapsulation
//
// Example:
//
//	handler := middleware.RequestID(
//	    middleware.Recovery(
//	        middleware.AccessLog(
//	            myHandler,
//	        ),
//	    ),
//	)
package middleware
