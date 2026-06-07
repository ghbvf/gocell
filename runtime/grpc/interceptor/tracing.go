package interceptor

import (
	"context"
	"strings"

	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// w3cGRPCPropagator and b3GRPCPropagator mirror the HTTP middleware's
// W3C-then-b3 trace-context extraction so a trace started by an upstream HTTP
// service continues across a gRPC hop. They are the standard stateless OTel
// propagators selected over a metadata carrier (below).
var (
	w3cGRPCPropagator propagation.TextMapPropagator = propagation.TraceContext{}
	b3GRPCPropagator  propagation.TextMapPropagator = b3.New()
)

// UnaryTracing returns an interceptor that starts a wrapper.Span per RPC named
// by the full method (leading slash stripped), records rpc.* attributes, and on
// error records the error and sets the span status. Error redaction is owned by
// the otelSpan sink (SPAN-RECORD-ERROR-SEAL-01) — call sites pass the raw error.
// Span attributes go through wrapper.Attr (not raw attribute.String), so the
// adapters/otel safeStringAttr redaction funnel applies automatically — this
// interceptor does not touch the SPAN-SETATTR-REDACT-01 callsite set. It mirrors
// runtime/http/middleware.Tracing. A nil tracer degrades to NoopTracer.
func UnaryTracing(tracer wrapper.Tracer) grpc.UnaryServerInterceptor {
	if tracer == nil {
		tracer = wrapper.NoopTracer{}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, span := startRPCSpan(ctx, tracer, info.FullMethod)
		defer span.End()
		resp, err := handler(ctx, req)
		finishRPCSpan(span, err)
		return resp, err
	}
}

// startRPCSpan is the transport-shape-agnostic span-open core shared by
// UnaryTracing and StreamTracing (PR-10 #1153): it continues any upstream trace
// from the incoming metadata, starts a span named by the full method, and sets
// the rpc.system/service/method attributes. The caller must `defer span.End()`.
func startRPCSpan(ctx context.Context, tracer wrapper.Tracer, fullMethod string) (context.Context, wrapper.Span) {
	ctx = extractGRPCTraceContext(ctx)
	ctx, span := tracer.Start(ctx, strings.TrimPrefix(fullMethod, "/"))
	service, method := splitFullMethod(fullMethod)
	span.SetAttributes(
		wrapper.Attr{Key: "rpc.system", Value: "grpc"},
		wrapper.Attr{Key: "rpc.service", Value: service},
		wrapper.Attr{Key: "rpc.method", Value: method},
	)
	return ctx, span
}

// finishRPCSpan is the transport-shape-agnostic span-close core shared by
// UnaryTracing and StreamTracing: it records the final gRPC status code and, on
// error, the (sink-redacted) error and an error span status.
func finishRPCSpan(span wrapper.Span, err error) {
	code := status.Code(err)
	span.SetAttributes(wrapper.Attr{Key: "rpc.grpc.status_code", Value: int64(code)})
	if err != nil {
		// status.Code never returns codes.OK for a non-nil error, so a failing
		// RPC always marks the span as error. The raw error is redacted at the
		// otelSpan sink (SPAN-RECORD-ERROR-SEAL-01).
		span.RecordError(err)
		span.SetStatus(wrapper.StatusError, code.String())
	}
}

// splitFullMethod splits "/pkg.Service/Method" into ("pkg.Service", "Method").
func splitFullMethod(full string) (service, method string) {
	full = strings.TrimPrefix(full, "/")
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i], full[i+1:]
	}
	return full, ""
}

// extractGRPCTraceContext continues an upstream trace carried in the incoming
// gRPC metadata. W3C traceparent takes precedence; b3 is the fallback. The
// extracted remote trace id is mirrored into ctxkeys so the wrapper.Tracer
// reuses it instead of starting a new root (matching the HTTP path).
func extractGRPCTraceContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	carrier := metadataCarrier(md)

	ctx = withExtractedRemoteSpanContext(w3cGRPCPropagator.Extract(ctx, carrier))
	if hasRemoteSpanContext(ctx) {
		return ctx
	}
	return withExtractedRemoteSpanContext(b3GRPCPropagator.Extract(ctx, carrier))
}

// withExtractedRemoteSpanContext mirrors a *remote* upstream trace id into
// ctxkeys so the wrapper.Tracer reuses it as the parent instead of starting a
// new root. Only remote span contexts qualify — a locally-started span context
// must not be treated as cross-service propagation. Mirrors the same-named
// helper in runtime/http/middleware/trace_propagation.go; keep them in sync.
func withExtractedRemoteSpanContext(ctx context.Context) context.Context {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() || !spanCtx.IsRemote() {
		return ctx
	}
	return ctxkeys.WithTraceID(ctx, spanCtx.TraceID().String())
}

// hasRemoteSpanContext reports whether ctx already carries a valid remote span
// context — used to short-circuit the W3C-then-b3 fallback once W3C succeeds.
func hasRemoteSpanContext(ctx context.Context) bool {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	return spanCtx.IsValid() && spanCtx.IsRemote()
}

// metadataCarrier adapts gRPC metadata to propagation.TextMapCarrier. gRPC
// metadata keys are lowercase, which is why the HTTP propagation.HeaderCarrier
// (which canonicalises keys) cannot be reused directly here.
type metadataCarrier metadata.MD

func (c metadataCarrier) Get(key string) string {
	if vals := metadata.MD(c).Get(key); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func (c metadataCarrier) Set(key, value string) { metadata.MD(c).Set(key, value) }

func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
