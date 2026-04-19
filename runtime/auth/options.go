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
	passwordResetChangeEndpointHint func() string                            // nil = no hint in 403 body
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

// WithPasswordResetChangeEndpointHintFn sets a getter closure that is called
// at request time to obtain the change_password_endpoint hint. Use this when
// the hint value is not known at middleware install time (e.g. it is derived
// by FinalizeAuth after RegisterRoutes completes).
//
// When fn is nil the option is a no-op.
func WithPasswordResetChangeEndpointHintFn(fn func() string) AuthOption {
	return func(c *authConfig) {
		if fn != nil {
			c.passwordResetChangeEndpointHint = fn
		}
	}
}

// WithPublicEndpointMatcher sets a compiled method-aware predicate for the
// auth middleware bypass check.
//
// Router.FinalizeAuth uses this option to install a lazy closure that reads
// the compiled public-route matcher — aggregated from every Cell's
// auth.Declare(mux, RouteDecl{Public: true}) call — at request time.
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
