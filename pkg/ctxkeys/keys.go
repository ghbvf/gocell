package ctxkeys

import "context"

// ctxKey is an unexported type to prevent key collisions with other packages.
type ctxKey string

const (
	correlationID ctxKey = "correlation_id"
	traceID       ctxKey = "trace_id"
	spanID        ctxKey = "span_id"
	traceParent   ctxKey = "traceparent"
	requestID     ctxKey = "request_id"
	realIP        ctxKey = "real_ip"
	actor         ctxKey = "actor_id"
	subject       ctxKey = "subject_id"
	tenant        ctxKey = "tenant_id"
	session       ctxKey = "session_id"
)

// --- CorrelationID ---

// WithCorrelationID returns a new context carrying the given correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationID, id)
}

// CorrelationIDFrom extracts the correlation ID from ctx. The boolean indicates presence.
func CorrelationIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(correlationID).(string)
	return v, ok
}

// --- TraceID ---

// WithTraceID returns a new context carrying the given trace ID.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceID, id)
}

// TraceIDFrom extracts the trace ID from ctx. The boolean indicates presence.
func TraceIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(traceID).(string)
	return v, ok
}

// --- TraceParent ---

// WithTraceParent returns a new context carrying the W3C traceparent value.
func WithTraceParent(ctx context.Context, traceparent string) context.Context {
	return context.WithValue(ctx, traceParent, traceparent)
}

// TraceParentFrom extracts the W3C traceparent value from ctx.
// The boolean indicates presence.
func TraceParentFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(traceParent).(string)
	return v, ok
}

// --- SpanID ---

// WithSpanID returns a new context carrying the given span ID.
func WithSpanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, spanID, id)
}

// SpanIDFrom extracts the span ID from ctx. The boolean indicates presence.
func SpanIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(spanID).(string)
	return v, ok
}

// --- RequestID ---

// WithRequestID returns a new context carrying the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestID, id)
}

// RequestIDFrom extracts the request ID from ctx. The boolean indicates presence.
func RequestIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(requestID).(string)
	return v, ok
}

// --- RealIP ---

// WithRealIP returns a new context carrying the client's real IP address.
func WithRealIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, realIP, ip)
}

// RealIPFrom extracts the client's real IP from ctx. The boolean indicates presence.
func RealIPFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(realIP).(string)
	return v, ok
}

// --- Principal (OAuth/OIDC) ---

// WithActor returns a new context carrying the actor identifier (OAuth
// impersonator — the principal actually triggering the action; in non-
// impersonation flows equals the Subject).
func WithActor(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, actor, id)
}

// ActorFrom extracts the actor identifier from ctx. The boolean indicates presence.
func ActorFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(actor).(string)
	return v, ok
}

// WithSubject returns a new context carrying the subject identifier (OAuth
// subject-of-record — the user the action is performed on behalf of).
func WithSubject(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, subject, id)
}

// SubjectFrom extracts the subject identifier from ctx. The boolean indicates presence.
func SubjectFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(subject).(string)
	return v, ok
}

// WithTenant returns a new context carrying the tenant identifier.
func WithTenant(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, tenant, id)
}

// TenantFrom extracts the tenant identifier from ctx. The boolean indicates presence.
func TenantFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenant).(string)
	return v, ok
}

// WithSession returns a new context carrying the session identifier.
func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, session, id)
}

// SessionFrom extracts the session identifier from ctx. The boolean indicates presence.
func SessionFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(session).(string)
	return v, ok
}
