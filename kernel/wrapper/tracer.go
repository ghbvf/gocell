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

// panicIfNotSetTracer is the zero-value sentinel. It panics immediately on
// Start() so that a missing SetTracer call surfaces at the first request, not
// silently emitting no-op spans.
//
// ref: slog.Default() / otel.GetTracerProvider() — package-level global with
// explicit registration; unregistered state panics fast rather than silently
// degrading.
type panicIfNotSetTracer struct{}

func (panicIfNotSetTracer) Start(_ context.Context, _ string, _ ...Attr) (context.Context, Span) {
	panic("kernel/wrapper: tracer not set — runtime.bootstrap must call wrapper.SetTracer before serving")
}

// tracer is the package-level singleton. Zero value is panicIfNotSetTracer so
// any request before SetTracer panics immediately, surfacing wiring errors
// on day 0. runtime/bootstrap calls SetTracer(b.tracer) during startup.
var tracer Tracer = panicIfNotSetTracer{} //nolint:gochecknoglobals

// SetTracer installs the package-level tracer. t must not be nil. Call once
// during process startup (e.g. from runtime/bootstrap.phase1LoadConfig or
// equivalent). Subsequent calls replace the active tracer — valid for testing
// but should not be needed in production.
//
// ref: slog.SetDefault / otel.SetTracerProvider — package-level setter pattern.
func SetTracer(t Tracer) {
	if t == nil {
		panic("kernel/wrapper.SetTracer: t must not be nil")
	}
	tracer = t
}

// NoopTracer is a zero-allocation Tracer. Use it in tests that exercise the
// wrapper path without an adapter dependency, or as the fallback when no
// runtime tracer is wired (runtime/bootstrap falls back to NoopTracer{} with
// a slog.Warn when WithTracer is not supplied).
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
type noopSpan struct{}

func (noopSpan) SetAttributes(_ ...Attr)          {}
func (noopSpan) RecordError(_ error)              {}
func (noopSpan) SetStatus(_ StatusCode, _ string) {}
func (noopSpan) End()                             {}
func (noopSpan) SetName(_ string)                 {}

var noopSpanInstance Span = noopSpan{}

// SpanRenamer is an optional interface that a Span implementation MAY
// support when the final span name is only known after the handler / routing
// completes (e.g. chi matches the path template post-ServeHTTP). Helpers
// that want to stay compatible with Span implementations lacking rename
// support should use SetSpanName instead of a direct type assertion.
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

// resetTracerForTest restores the package-level tracer to panicIfNotSetTracer.
// Only callable from _test.go files in package wrapper or wrapper_test.
// Use t.Cleanup(wrapper.resetTracerForTest) in tests that call SetTracer.
func resetTracerForTest() {
	tracer = panicIfNotSetTracer{}
}
