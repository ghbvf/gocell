// Package logging provides a slog.Handler that enriches log records with
// trace_id, span_id, request_id, correlation_id, and cell_id from the
// request context.
// Supports JSON and text output formats.
//
// ref: go stdlib log/slog — Handler interface + Handle(context, Record) pattern
// Adopted: wrapping an inner slog.Handler, extracting context values in Handle.
// Deviated: adds GoCell-specific ctxkeys (request_id, correlation_id, cell_id).
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"

	// Cell-model keys (cell/slice/journey) live in kernel/ctxkeys; generic
	// obs/networking keys (request/trace/span/correlation/real IP) live in
	// pkg/ctxkeys. pkg/ must not depend on kernel/, so dual-import is the
	// intentional resolution — do not collapse into a single package.
	kctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/redaction"
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

// contextHandler wraps an inner slog.Handler, enriches records with context
// values, and provides fail-closed sink-side redaction. Beyond context
// injection, every Handle call redacts the message with redaction.RedactString
// and every attr with redaction.RedactSlogAttr. WithAttrs pre-redacts attrs at
// bind time so that logger.With("password", v) is safe before any Handle call.
// This makes the handler the process-global redaction barrier: even call sites
// that omit explicit redaction helpers are protected.
type contextHandler struct {
	inner slog.Handler
}

// NewHandler creates a slog.Handler that redacts every record (fail-closed)
// and enriches it with GoCell context values. It builds a fresh JSON/Text sink
// writing to opts.Writer (default os.Stdout).
//
// Security contract: every Handle call scrubs the log message with
// redaction.RedactString and every attr with redaction.RedactSlogAttr (two-layer:
// key-aware mask + free-form regex). WithAttrs pre-redacts at bind time. This
// makes contextHandler the process-global redaction barrier — call-site
// redaction helpers are defense-in-depth only.
//
// Production entry points must seal the process-global slog default before any
// log emission:
//
//	slog.SetDefault(slog.New(logging.NewHandler(logging.Options{...})))
//
// This is enforced by archtest SLOG-HANDLER-SEALED-FUNNEL-01 A3 in two
// segments: generated assemblies are sealed by the assembly template and locked
// by generated-verify + marker-derived archtest coverage (Hard, #1401
// delivered); hand-written entry points are sealed by a bounded allowlist
// (Medium, residual tracked at gh #1424).
//
// Building a fresh sink (rather than wrapping the stdlib default) avoids the
// slog↔log recursion that wrapping the default handler would trigger.
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
// Every attr in the record — including the message — is redacted before
// forwarding to the inner handler. Context-injected fields are framework-
// trusted (trace/request IDs, cell_id) and are appended after redaction.
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, redaction.RedactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redaction.RedactSlogAttr(a))
		return true
	})
	ctxAttrs := extractContextAttrs(ctx)
	if len(ctxAttrs) > 0 {
		nr.AddAttrs(ctxAttrs...)
	}
	return h.inner.Handle(ctx, nr)
}

// WithAttrs returns a new handler with the given attributes pre-applied.
// Attributes are redacted at bind time to cover the logger.With(...) path,
// which pre-binds attrs before any Handle call.
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redaction.RedactSlogAttr(a)
	}
	return &contextHandler{inner: h.inner.WithAttrs(redacted)}
}

// WithGroup returns a new handler with the given group name. Subsequent Handle
// calls process attrs inside this group through the same RedactSlogAttr pipeline
// as top-level attrs — the group prefix does not bypass attr-level redaction.
func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}

// extractContextAttrs extracts framework-injected observability fields from ctx.
// Trust boundary: only framework-internal, enumerated fields are extracted
// (trace_id, span_id, request_id, correlation_id, cell_id, contract_id). These
// are all non-user-input, non-sensitive enumeration values — they are not
// passed through RedactSlogAttr. Before adding any new field here, confirm it
// belongs to this trusted-enumeration set; any field carrying user-supplied or
// credential-adjacent data must instead be added via a regular slog attr and
// will be redacted by the Handle/WithAttrs pipeline.
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
	if v, ok := kctxkeys.CellIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("cell_id", v))
	}
	// contract_id: written by wrapper.HTTPHandler / WrapConsumer when the
	// request/event enters a contract-bound path; absent on non-migrated
	// (non-contract) routes. Skipping empty aligns with
	// every other field above.
	if v, ok := kctxkeys.ContractIDFrom(ctx); ok && v != "" {
		attrs = append(attrs, slog.String("contract_id", v))
	}

	return attrs
}
