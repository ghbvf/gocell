package auth

import "net/http"

// BootstrapCredentials carries the env-driven HTTP Basic Auth credentials
// used to protect the first-admin setup endpoint.
type BootstrapCredentials struct {
	Username []byte
	Password []byte
}

// bootstrapRateLimiter decides whether a request identified by key should be allowed.
// This is a local interface alias for middleware.RateLimiter to avoid an import cycle
// (runtime/http/middleware already imports runtime/auth via access_log.go).
type bootstrapRateLimiter interface {
	Allow(key string) bool
}

// newBootstrapMiddleware constructs the HTTP middleware chain for bootstrap
// authentication. To be implemented in Batch 1 / Agent-B.
func newBootstrapMiddleware(creds BootstrapCredentials, limiter bootstrapRateLimiter) func(http.Handler) http.Handler {
	panic("newBootstrapMiddleware: not implemented; see Batch 1 / Agent-B")
}
