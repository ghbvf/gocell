package auth

import "net/http"

// BootstrapRateLimiter is an exported alias of the internal bootstrapRateLimiter
// interface so black-box test code can implement fakes without importing internal types.
type BootstrapRateLimiter = bootstrapRateLimiter

// ExportedNewBootstrapMiddleware exposes newBootstrapMiddleware for black-box tests in
// runtime/auth_test package. Panics until Batch 1 / Agent-B implements it.
func ExportedNewBootstrapMiddleware(creds BootstrapCredentials, limiter BootstrapRateLimiter) func(http.Handler) http.Handler {
	return newBootstrapMiddleware(creds, limiter)
}
