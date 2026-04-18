package auth

import (
	"context"
	"log/slog"
	"net/http"
)

// AuthOption configures auth middleware behavior.
type AuthOption func(*authConfig)

type authConfig struct {
	logger                          *slog.Logger
	metrics                         *AuthMetrics
	publicMatcher                   func(*http.Request) bool                 // nil = use []string publicEndpoints path
	delegatedMatcher                func(*http.Request) bool                 // nil = no delegated paths
	passwordResetExempt             func(method string, urlPath string) bool // nil = fail-closed (nothing exempt)
	passwordResetChangeEndpointHint string                                   // empty = no hint in 403 body
}

func defaultAuthConfig() authConfig {
	return authConfig{logger: slog.Default()}
}

// WithLogger sets the logger for auth middleware.
func WithLogger(l *slog.Logger) AuthOption {
	return func(c *authConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithMetrics sets the AuthMetrics for auth middleware.
func WithMetrics(m *AuthMetrics) AuthOption {
	return func(c *authConfig) { c.metrics = m }
}

// WithPasswordResetExemptMatcher installs a (method, path) → bool predicate
// used by the password-reset gate. When a token carries
// password_reset_required=true, only requests for which the matcher returns
// true are allowed through; everything else returns 403.
//
// If no matcher is supplied, the gate rejects every authenticated request —
// fail-closed. Composition roots must opt in to exempt paths (typically the
// change-password and logout endpoints) so that runtime/auth stays free of
// cell-specific path knowledge (F6 decoupling).
func WithPasswordResetExemptMatcher(fn func(method, urlPath string) bool) AuthOption {
	return func(c *authConfig) { c.passwordResetExempt = fn }
}

// WithPasswordResetChangeEndpointHint sets the "METHOD /path" string emitted
// as details.change_password_endpoint in the 403 ERR_AUTH_PASSWORD_RESET_REQUIRED
// response body — a navigational hint for clients that do not know which
// endpoint finishes the reset flow.
//
// Empty value (the default) omits the hint entirely, keeping runtime/auth
// free of any business-level path knowledge. Composition roots opt in
// explicitly; typically they pass the same change-password path they list
// via WithPasswordResetExemptEndpoints.
func WithPasswordResetChangeEndpointHint(hint string) AuthOption {
	return func(c *authConfig) { c.passwordResetChangeEndpointHint = hint }
}

// WithPublicEndpointMatcher sets a compiled method-aware predicate for the
// auth middleware bypass check. When provided, this takes precedence over the
// publicEndpoints []string parameter passed to AuthMiddleware.
//
// Use this option when wiring through router.WithPublicEndpoints so that auth
// bypass is keyed on (method + path), not path alone.
//
// ref: otelhttp WithPublicEndpointFn per-request predicate shape
func WithPublicEndpointMatcher(fn func(*http.Request) bool) AuthOption {
	return func(c *authConfig) {
		c.publicMatcher = fn
	}
}

// WithDelegatedMatcher installs a per-request predicate that marks paths where
// JWT authentication is delegated to a downstream middleware (e.g. a
// service-token guard or mTLS check). When the predicate returns true, the auth
// middleware calls next.ServeHTTP directly — no JWT verification, no 401 — and
// lets the downstream middleware claim authentication authority.
//
// Distinct from WithPublicEndpointMatcher (truly unauthenticated): delegated
// means "JWT is not the right credential for this route — defer to the guard
// installed further down the chain."
//
// The companion option WithDelegatedEndpoints accepts a "METHOD /path" slice and
// compiles it into a predicate of this shape automatically.
//
// ref: Kratos middleware/selector matcher-based middleware selection
// ref: go-zero rest/engine route-group JWT metadata
func WithDelegatedMatcher(fn func(*http.Request) bool) AuthOption {
	return func(c *authConfig) {
		c.delegatedMatcher = fn
	}
}

type loggerCtxKey struct{}

func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
