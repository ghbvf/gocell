// Package otel provides an OpenTelemetry adapter that implements the
// runtime/observability/tracing.Tracer interface using the OTel SDK.
//
// It bridges GoCell's lightweight tracing abstraction to the full
// OpenTelemetry pipeline (OTLP/gRPC export, W3C TraceContext propagation,
// configurable sampling). Context keys (trace_id, span_id) are propagated
// via pkg/ctxkeys so that slog correlation works out of the box.
//
// ref: go.opentelemetry.io/otel -- TracerProvider, Tracer, Span API
// Adopted: OTel SDK TracerProvider lifecycle, OTLP gRPC exporter.
// Deviated: wrapped behind runtime/tracing.Tracer so cells remain
// decoupled from OTel imports.
package otel
