// Package ctxkeys provides typed context keys and helper functions for
// propagating GoCell identifiers (cell, slice, correlation, journey, trace,
// span) through context.Context.
package ctxkeys

import "context"

// ctxKey is an unexported type to prevent key collisions with other packages.
type ctxKey string

// Context key constants used across the GoCell framework.
const (
	CellID        ctxKey = "cell_id"
	SliceID       ctxKey = "slice_id"
	CorrelationID ctxKey = "correlation_id"
	JourneyID     ctxKey = "journey_id"
	TraceID       ctxKey = "trace_id"
	SpanID        ctxKey = "span_id"
	RequestID     ctxKey = "request_id"
	RealIP        ctxKey = "real_ip"
	Subject       ctxKey = "subject"
)

// --- CellID ---

// WithCellID returns a new context carrying the given cell ID.
func WithCellID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, CellID, id)
}

// CellIDFrom extracts the cell ID from ctx. The boolean indicates presence.
func CellIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(CellID).(string)
	return v, ok
}

// --- SliceID ---

// WithSliceID returns a new context carrying the given slice ID.
func WithSliceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, SliceID, id)
}

// SliceIDFrom extracts the slice ID from ctx. The boolean indicates presence.
func SliceIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(SliceID).(string)
	return v, ok
}

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

// --- JourneyID ---

// WithJourneyID returns a new context carrying the given journey ID.
func WithJourneyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, JourneyID, id)
}

// JourneyIDFrom extracts the journey ID from ctx. The boolean indicates presence.
func JourneyIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(JourneyID).(string)
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

// --- Subject ---

// WithSubject returns a new context carrying the authenticated subject identifier.
func WithSubject(ctx context.Context, sub string) context.Context {
	return context.WithValue(ctx, Subject, sub)
}

// SubjectFrom extracts the authenticated subject from ctx. The boolean indicates presence.
func SubjectFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(Subject).(string)
	return v, ok
}
