package auth

import (
	"context"
	"log/slog"
	"net/http"
)

// AuthOption configures auth middleware behavior.
type AuthOption func(*authConfig)

type authConfig struct {
	logger        *slog.Logger
	metrics       *AuthMetrics
	publicMatcher func(*http.Request) bool // nil = use []string publicEndpoints path
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
