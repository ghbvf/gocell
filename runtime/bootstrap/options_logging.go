package bootstrap

// options_logging.go — WithLogging option for configuring the sink-side
// redacting slog.Handler that bootstrap.Run installs as slog.Default().
//
// The handler itself (logging.NewHandler) always wraps a redacting
// contextHandler; this option controls only the output format, level, and
// writer — redaction is not configurable and cannot be disabled.
//
// Follows the "累加式 builder option" pattern (runtime-api.md): a nil or
// zero-value WithLogging call is a silent noop; the last non-zero call wins.
// Redaction cannot be opted out — the handler always runs redaction.

import (
	"github.com/ghbvf/gocell/runtime/observability/logging"
)

// WithLogging configures the logging options (format, level, writer) used by
// bootstrap.Run when it installs the sink-side redacting slog.Handler as the
// process-global slog.Default().
//
// This is a cumulative builder option: the last non-zero call wins. Redaction
// is always active and cannot be disabled. When WithLogging is never called,
// the default Options{} is used (FormatJSON, LevelInfo, os.Stdout).
//
// ref: runtime-api.md — 累加式 builder option pattern.
func WithLogging(opts logging.Options) Option {
	return func(b *Bootstrap) {
		b.loggingOpts = opts
		b.loggingConfigured = true
	}
}
