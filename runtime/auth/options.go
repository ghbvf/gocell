package auth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/clock"
)

// AuthOption configures auth middleware behavior.
type AuthOption func(*authConfig)

type authConfig struct {
	clock                           clock.Clock
	logger                          *slog.Logger
	metrics                         *AuthMetrics
	publicMatcher                   func(*http.Request) bool                 // nil = use []string publicEndpoints path
	passwordResetExempt             func(method string, urlPath string) bool // nil = fail-closed (nothing exempt)
	passwordResetChangeEndpointHint func() string                            // nil = no hint in 403 body
}

func defaultAuthConfig() authConfig {
	return authConfig{clock: clock.Real(), logger: slog.Default()}
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
// at request time to obtain the changePasswordEndpoint hint. Use this when
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
// auth.Mount(mux, Route{Public: true}) call — at request time.
//
// ref: otelhttp WithPublicEndpointFn per-request predicate shape
func WithPublicEndpointMatcher(fn func(*http.Request) bool) AuthOption {
	return func(c *authConfig) {
		c.publicMatcher = fn
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
