// Package tracing provides a Tracer interface and HTTP middleware for
// distributed tracing. The default implementation generates trace/span IDs
// and propagates them via context. Production deployments should use an
// adapter (e.g., adapters/otel) that integrates with OpenTelemetry.
//
// ref: go.opentelemetry.io/otel — Tracer/Span API, W3C TraceContext propagation
// Adopted: Tracer/Span interface shape, trace_id+span_id in context.
// Deviated: lightweight stdlib-only implementation; OTel integration lives
// in adapters/ to keep runtime/ dependency-free.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// Span represents a unit of work in a trace.
type Span interface {
	// End completes the span.
	End()
	// SetAttribute records a key-value pair on the span.
	SetAttribute(key string, value any)
	// TraceID returns the trace identifier.
	TraceID() string
	// SpanID returns the span identifier.
	SpanID() string
}

// SpanRecorder is an optional interface that Span implementations may support
// for recording errors and setting status. Use the SpanRecordError and
// SpanSetStatus helper functions which handle type-assertion gracefully.
type SpanRecorder interface {
	// RecordError adds an error event to the span for diagnostics.
	RecordError(err error)
	// SetStatus sets the span's status. When isError is true the span is
	// marked as failed with the given description; otherwise it is marked OK.
	SetStatus(isError bool, description string)
}

// SpanRecordError records an error on the span if it implements SpanRecorder.
// Spans that do not support error recording are silently skipped.
func SpanRecordError(s Span, err error) {
	if r, ok := s.(SpanRecorder); ok {
		r.RecordError(err)
	}
}

// SpanSetStatus sets the status on the span if it implements SpanRecorder.
// Spans that do not support status setting are silently skipped.
func SpanSetStatus(s Span, isError bool, description string) {
	if r, ok := s.(SpanRecorder); ok {
		r.SetStatus(isError, description)
	}
}

// Tracer creates spans.
type Tracer interface {
	// Start creates a new span with the given name. The returned context
	// carries the span and its trace/span IDs.
	Start(ctx context.Context, name string) (context.Context, Span)
}

// simpleTracer is a lightweight Tracer that generates random IDs.
type simpleTracer struct {
	name string
}

// NewTracer creates a simple Tracer that generates random trace/span IDs.
// For OpenTelemetry integration, use adapters/otel.
func NewTracer(name string) Tracer {
	return &simpleTracer{name: name}
}

func (t *simpleTracer) Start(ctx context.Context, name string) (context.Context, Span) {
	traceID := generateID(16)
	spanID := generateID(8)

	// Check if parent trace ID exists in context.
	if parentTrace, ok := ctxkeys.TraceIDFrom(ctx); ok && parentTrace != "" {
		traceID = parentTrace
	}

	s := &simpleSpan{
		traceID: traceID,
		spanID:  spanID,
		name:    name,
	}

	ctx = ctxkeys.WithTraceID(ctx, traceID)
	ctx = ctxkeys.WithSpanID(ctx, spanID)
	return ctx, s
}

// simpleSpan is a lightweight span implementation.
type simpleSpan struct {
	traceID string
	spanID  string
	name    string
	attrs   map[string]any
}

func (s *simpleSpan) End() {}

func (s *simpleSpan) SetAttribute(key string, value any) {
	if s.attrs == nil {
		s.attrs = make(map[string]any)
	}
	s.attrs[key] = value
}

func (s *simpleSpan) TraceID() string { return s.traceID }
func (s *simpleSpan) SpanID() string  { return s.spanID }

// generateID creates a random hex-encoded ID of the given byte length.
func generateID(byteLen int) string {
	buf := make([]byte, byteLen)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
