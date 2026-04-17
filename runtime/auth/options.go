package auth

import (
	"context"
	"log/slog"
)

// AuthOption configures auth middleware behavior.
type AuthOption func(*authConfig)

type authConfig struct {
	logger  *slog.Logger
	metrics *AuthMetrics
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
