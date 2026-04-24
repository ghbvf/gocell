package ctxkeys

import "context"

// ctxKey is an unexported type to prevent key collisions with other packages.
type ctxKey string

// Generic observability and networking context keys.
const (
	CorrelationID ctxKey = "correlation_id"
	TraceID       ctxKey = "trace_id"
	SpanID        ctxKey = "span_id"
	TraceParent   ctxKey = "traceparent"
	RequestID     ctxKey = "request_id"
	RealIP        ctxKey = "real_ip"
)

// --- CorrelationID ---

// WithCorrelationID returns a new context carrying the given correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, CorrelationID, id)
}

// CorrelationIDFrom extracts the correlation ID from ctx. The boolean indicates presence.
func CorrelationIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(CorrelationID).(string)
	return v, ok
}

// --- TraceID ---

// WithTraceID returns a new context carrying the given trace ID.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, TraceID, id)
}

// TraceIDFrom extracts the trace ID from ctx. The boolean indicates presence.
func TraceIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(TraceID).(string)
	return v, ok
}

// --- TraceParent ---

// WithTraceParent returns a new context carrying the W3C traceparent value.
func WithTraceParent(ctx context.Context, traceparent string) context.Context {
	return context.WithValue(ctx, TraceParent, traceparent)
}

// TraceParentFrom extracts the W3C traceparent value from ctx.
// The boolean indicates presence.
func TraceParentFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(TraceParent).(string)
	return v, ok
}

// --- SpanID ---

// WithSpanID returns a new context carrying the given span ID.
func WithSpanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, SpanID, id)
}

// SpanIDFrom extracts the span ID from ctx. The boolean indicates presence.
func SpanIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(SpanID).(string)
	return v, ok
}

// --- RequestID ---

// WithRequestID returns a new context carrying the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, RequestID, id)
}

// RequestIDFrom extracts the request ID from ctx. The boolean indicates presence.
func RequestIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(RequestID).(string)
	return v, ok
}

// --- RealIP ---

// WithRealIP returns a new context carrying the client's real IP address.
func WithRealIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, RealIP, ip)
}

// RealIPFrom extracts the client's real IP from ctx. The boolean indicates presence.
func RealIPFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(RealIP).(string)
	return v, ok
}
