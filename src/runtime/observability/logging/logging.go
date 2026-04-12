// Package logging provides a slog.Handler that enriches log records with
// trace_id, span_id, request_id, correlation_id, and cell_id from the
// request context.
// Supports JSON and text output formats.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// Format specifies the log output format.
type Format string

const (
	// FormatJSON outputs structured JSON logs.
	FormatJSON Format = "json"
	// FormatText outputs human-readable text logs.
	FormatText Format = "text"
)

// Options configures the logging handler.
type Options struct {
	// Level is the minimum log level. Defaults to slog.LevelInfo.
	Level slog.Leveler
	// Format selects between JSON and text output. Defaults to FormatJSON.
	Format Format
	// Writer is the output destination. Defaults to os.Stdout.
	Writer io.Writer
}

// contextHandler wraps an inner slog.Handler and enriches records with
// context values.
type contextHandler struct {
	inner slog.Handler
}

// NewHandler creates a slog.Handler that extracts GoCell context values and
// adds them as structured fields to every log record.
func NewHandler(opts Options) slog.Handler {
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}

	handlerOpts := &slog.HandlerOptions{Level: opts.Level}

	var inner slog.Handler
	switch opts.Format {
	case FormatText:
		inner = slog.NewTextHandler(opts.Writer, handlerOpts)
	default:
		inner = slog.NewJSONHandler(opts.Writer, handlerOpts)
	}

	return &contextHandler{inner: inner}
}

// Enabled reports whether the handler handles records at the given level.
func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle enriches the record with context values before delegating.
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := extractContextAttrs(ctx)
	if len(attrs) > 0 {
		r.AddAttrs(attrs...)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a new handler with the given attributes pre-applied.
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup returns a new handler with the given group name.
func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}

func extractContextAttrs(ctx context.Context) []slog.Attr {
	var attrs []slog.Attr

	if v, ok := ctxkeys.TraceIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("trace_id", v))
	}
	if v, ok := ctxkeys.SpanIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("span_id", v))
	}
	if v, ok := ctxkeys.RequestIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("request_id", v))
	}
	if v, ok := ctxkeys.CorrelationIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("correlation_id", v))
	}
	if v, ok := ctxkeys.CellIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("cell_id", v))
	}

	return attrs
}
