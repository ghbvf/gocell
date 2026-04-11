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
// # BFF Middleware Ordering
//
// For BFF (Browser-Facing) deployments with cookie-based sessions, the
// middleware chain order is critical:
//
//	CookieSession → CSRF → AuthMiddleware → handler
//
//   - CookieSession runs first: reads the session cookie and injects an
//     Authorization: Bearer header so that downstream middleware sees a
//     standard JWT.
//   - CSRF runs second: validates Origin/Referer/Sec-Fetch-Site. This must
//     run before AuthMiddleware because CSRF rejection is a 403 (not 401).
//   - AuthMiddleware runs third: verifies the JWT (from cookie or header)
//     and injects Claims into the request context.
//
// Example:
//
//	sessMW := middleware.MustCookieSession(sessCfg)
//	csrfMW := middleware.CSRF(csrfCfg)
//	authMW := auth.AuthMiddleware(verifier, publicEndpoints)
//
//	rtr.Route("/api/v1", func(r cell.RouteMux) {
//	    protected := r.With(sessMW, csrfMW, authMW)
//	    protected.Handle("/resource", resourceHandler)
//	})
package middleware
