package wrapper

import "context"

// Attr is a key-value pair recorded on a Span. Value is any so adapters can
// handle type-specific conversions (OTel attribute.Key, slog fields, ...);
// producers should prefer string / int64 / bool for cross-adapter portability.
type Attr struct {
	Key   string
	Value any
}

// StatusCode is the span-level success / failure marker. Adapters translate
// these ordinals into backend-specific status codes (OTel `codes.Ok`/`codes.Error`,
// Zipkin error tags, etc.).
type StatusCode int

const (
	// StatusOK indicates the span completed successfully. It is the zero value
	// so Span implementations default to OK unless SetStatus is called.
	StatusOK StatusCode = 0
	// StatusError marks the span as failed; adapters SHOULD surface this as
	// an error-level classification in their backend.
	StatusError StatusCode = 1
)

// Span represents a single unit of work in a trace. Implementations MUST be
// safe for concurrent use — middleware and handlers running on the same
// request may emit attributes from multiple goroutines (ResponseWriter
// observers, error recorders, etc.).
type Span interface {
	// SetAttributes records key-value pairs on the span.
	SetAttributes(attrs ...Attr)
	// RecordError attaches an error event to the span. Recording an error
	// does not by itself mark the span as failed — callers should also call
	// SetStatus(StatusError, "...") when the error is terminal.
	RecordError(err error)
	// SetStatus records the final success/failure status for the span. The
	// last SetStatus call wins. Description is free-form; adapters MAY
	// truncate long values.
	SetStatus(code StatusCode, description string)
	// End finalises the span. Calls after End are no-ops; implementations
	// SHOULD ignore post-End mutations silently.
	End()
}

// Tracer creates spans. The returned context carries the span so downstream
// code may read the active trace identity via TraceIDFromContext /
// SpanIDFromContext.
//
// Implementations that continue a trace present on the input context (e.g.
// an OTel SDK) SHOULD do so; the NoopTracer accepts any context unchanged.
type Tracer interface {
	Start(ctx context.Context, spanName string, attrs ...Attr) (context.Context, Span)
}

// NoopTracer is a zero-allocation Tracer. Used by WrapConsumer as the
// fallback when no runtime tracer is wired (nil Tracer argument), and by
// tests that exercise the wrapper path without an adapter dependency.
//
// HTTPHandler no longer creates spans (the outer HTTP Tracing middleware
// owns the single request span), so it does not reference Tracer at all —
// NoopTracer is only used on the consumer side (WrapConsumer) where the
// event router is the span owner and bootstrap is the tracer injector.
type NoopTracer struct{}

// Compile-time: NoopTracer implements Tracer with a value receiver so zero
// allocations happen at the common `wrapper.NoopTracer{}` call site.
var _ Tracer = NoopTracer{}

// Start returns the context unchanged and a noopSpan singleton — zero
// allocations in the hot path.
func (NoopTracer) Start(ctx context.Context, _ string, _ ...Attr) (context.Context, Span) {
	return ctx, noopSpanInstance
}

// noopSpan is a shared singleton so Span method calls never allocate.
// All methods are intentional no-ops: this is a zero-allocation do-nothing
// span returned by NoopTracer when tracing is disabled.
type noopSpan struct{}

func (noopSpan) SetAttributes(_ ...Attr)          {} // noop: see noopSpan godoc
func (noopSpan) RecordError(_ error)              {} // noop: see noopSpan godoc
func (noopSpan) SetStatus(_ StatusCode, _ string) {} // noop: see noopSpan godoc
func (noopSpan) End()                             {} // noop: see noopSpan godoc
func (noopSpan) SetName(_ string)                 {} // noop: see noopSpan godoc

var noopSpanInstance Span = noopSpan{}

// SpanRenamer is an optional interface that a Span implementation MAY
// support when the final span name is only known after the handler / routing
// completes (e.g. chi matches the path template post-ServeHTTP). Helpers
// that want to stay compatible with Span implementations lacking rename
// support should use SetSpanName instead of a direct type assertion.
//
// NoopTracer's span implements SetName as a silent no-op, so callers can
// invoke SetSpanName unconditionally regardless of whether a real tracer
// is wired — there is no "rename unsupported" branch to worry about when
// tracing is disabled.
//
// ref: riandyrn/otelchi middleware.go — chi routes are known after ServeHTTP
// so span.name is adjusted in two phases.
type SpanRenamer interface {
	SetName(name string)
}

// SetSpanName invokes SetName if the span implements SpanRenamer. Spans that
// do not support rename are silently skipped.
func SetSpanName(s Span, name string) {
	if r, ok := s.(SpanRenamer); ok {
		r.SetName(name)
	}
}
