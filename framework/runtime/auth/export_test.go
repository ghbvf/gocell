package auth

import (
	"net/http"
)

// ExportedNewBootstrapMiddleware exposes NewBootstrapMiddleware for black-box
// tests in runtime/auth_test package.
func ExportedNewBootstrapMiddleware(creds BootstrapCredentials, limiter BootstrapRateLimiter) func(http.Handler) http.Handler {
	return NewBootstrapMiddleware(creds, limiter, nil)
}

// ExportedNewBootstrapMiddlewareWithHook exposes NewBootstrapMiddleware with
// an onAuthFail observer for black-box tests.
func ExportedNewBootstrapMiddlewareWithHook(
	creds BootstrapCredentials,
	limiter BootstrapRateLimiter,
	onAuthFail BootstrapAuthFailObserver,
) func(http.Handler) http.Handler {
	return NewBootstrapMiddleware(creds, limiter, onAuthFail)
}
