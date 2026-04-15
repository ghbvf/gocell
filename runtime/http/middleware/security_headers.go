package middleware

import "net/http"

// SecurityHeaders sets security-related response headers on every request:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Strict-Transport-Security: max-age=63072000; includeSubDomains; preload
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		next.ServeHTTP(w, r)
	})
}
