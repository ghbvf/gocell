// Package tracing provides a Tracer interface and HTTP middleware for
// distributed tracing. The default implementation generates random trace/span
// IDs and propagates them via context. Production deployments should use an
// OpenTelemetry-based adapter that integrates with OTLP collectors.
//
// Trace and span IDs are stored in the context via pkg/ctxkeys and are
// automatically picked up by the logging handler (runtime/observability/logging),
// which appends them as structured fields to every log record.
//
// ref: go.opentelemetry.io/otel — Tracer/Span API, W3C TraceContext propagation
// Adopted: Tracer/Span interface shape, trace_id + span_id in context.
// Deviated: lightweight stdlib-only default; OTel integration lives in
// adapters/ to keep runtime/ dependency-free.
//
// # Usage
//
//	tracer := tracing.NewTracer("my-cell")
//
//	// Add tracing middleware to the router:
//	r.Use(tracing.Middleware(tracer))
//
//	// Start a child span inside a handler or service:
//	ctx, span := tracer.Start(ctx, "session.validate")
//	defer span.End()
//	span.SetAttribute("session_id", sessionID)
//
// # Context Keys
//
// Use pkg/ctxkeys.TraceIDFrom and SpanIDFrom to read IDs from context in
// code that should not import this package directly.
package tracing
