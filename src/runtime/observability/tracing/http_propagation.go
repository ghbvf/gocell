package tracing

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var httpTextMapPropagator = propagation.NewCompositeTextMapPropagator(
	propagation.TraceContext{},
	b3.New(),
	propagation.Baggage{},
)

// ExtractHTTPContext restores a remote trace context from HTTP headers.
// When a valid upstream span context is present, the extracted trace/span IDs
// are mirrored into ctxkeys so both the lightweight tracer and OTel-backed
// tracer continue the same distributed trace.
func ExtractHTTPContext(ctx context.Context, header http.Header) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if header == nil {
		return ctx
	}

	ctx = httpTextMapPropagator.Extract(ctx, propagation.HeaderCarrier(header))

	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ctx
	}

	ctx = ctxkeys.WithTraceID(ctx, spanCtx.TraceID().String())
	ctx = ctxkeys.WithSpanID(ctx, spanCtx.SpanID().String())
	return ctx
}