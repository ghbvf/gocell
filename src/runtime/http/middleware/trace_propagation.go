package middleware

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var (
	w3cTraceContextPropagator propagation.TextMapPropagator = propagation.TraceContext{}
	b3TraceContextPropagator  propagation.TextMapPropagator = b3.New()
)

func extractTraceContext(ctx context.Context, header http.Header) context.Context {
	if header == nil {
		return ctx
	}

	ctx = extractTraceContextWithPropagator(ctx, header, w3cTraceContextPropagator)
	if hasRemoteSpanContext(ctx) {
		return ctx
	}

	return extractTraceContextWithPropagator(ctx, header, b3TraceContextPropagator)
}

func extractTraceContextWithPropagator(ctx context.Context, header http.Header, propagator propagation.TextMapPropagator) context.Context {
	ctx = propagator.Extract(ctx, propagation.HeaderCarrier(header))
	return withExtractedRemoteSpanContext(ctx)
}

// withExtractedRemoteSpanContext mirrors the upstream trace ID into ctxkeys
// so that simpleTracer.Start reuses it instead of generating a new root.
// Span ID is intentionally NOT pre-seeded — tracer.Start always creates a
// fresh server span, so any value written here would be immediately
// overwritten.
func withExtractedRemoteSpanContext(ctx context.Context) context.Context {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() || !spanCtx.IsRemote() {
		return ctx
	}

	return ctxkeys.WithTraceID(ctx, spanCtx.TraceID().String())
}

func hasRemoteSpanContext(ctx context.Context) bool {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	return spanCtx.IsValid() && spanCtx.IsRemote()
}
