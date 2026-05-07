// Package tracing provides a lightweight Tracer implementation and HTTP
// middleware glue. The canonical Tracer / Span interfaces are defined in
// kernel/wrapper; this package re-exports them so legacy callers can
// continue writing `tracing.Tracer` / `tracing.Span`, and supplies a stdlib-
// only simpleTracer useful for tests and demo-mode runs. Production
// deployments should wire the OTel adapter in adapters/otel.
//
// ref: go.opentelemetry.io/otel — Tracer/Span API, W3C TraceContext propagation
// Adopted: Tracer/Span interface shape, trace_id+span_id in context (shape now
// lives in kernel/wrapper to keep layering pure — runtime re-exports).
// Deviated: lightweight stdlib-only implementation; OTel integration lives
// in adapters/otel to keep runtime/ dependency-free.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// Tracer and Span re-export the canonical kernel types so callers that
// imported runtime/observability/tracing keep compiling. Prefer
// kernel/wrapper directly in new code.
type (
	// Tracer is a re-export of kernel/wrapper.Tracer.
	Tracer = wrapper.Tracer
	// Span is a re-export of kernel/wrapper.Span.
	Span = wrapper.Span
	// Attr is a re-export of kernel/wrapper.Attr.
	Attr = wrapper.Attr
)

// SpanSetName renames the span if it implements wrapper.SpanRenamer.
// Kept here for backwards compatibility with HTTP middleware that was
// written before the kernel interface existed. New code can call
// wrapper.SetSpanName directly.
func SpanSetName(s Span, name string) {
	wrapper.SetSpanName(s, name)
}

// SpanSetStatus is a thin shim over wrapper.Span.SetStatus that maps the
// legacy boolean to wrapper.StatusCode so existing callers compile.
func SpanSetStatus(s Span, isError bool, description string) {
	if isError {
		s.SetStatus(wrapper.StatusError, description)
		return
	}
	s.SetStatus(wrapper.StatusOK, description)
}

// Compile-time assertion: simpleSpan implements the kernel Span interface.
var _ Span = (*simpleSpan)(nil)

// simpleTracer is a stdlib-only Tracer useful for tests and demo-mode
// deployments that have no OTel backend wired.
type simpleTracer struct {
	name string
}

// NewTracer creates a simple Tracer that generates random trace/span IDs.
// For OpenTelemetry integration, use adapters/otel.
func NewTracer(name string) Tracer {
	return &simpleTracer{name: name}
}

func (t *simpleTracer) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	traceID := generateID(16)
	spanID := generateID(8)

	if parentTrace, ok := ctxkeys.TraceIDFrom(ctx); ok && parentTrace != "" {
		traceID = parentTrace
	}

	s := &simpleSpan{
		traceID: traceID,
		spanID:  spanID,
		name:    name,
	}
	if len(attrs) > 0 {
		s.SetAttributes(attrs...)
	}

	ctx = ctxkeys.WithTraceID(ctx, traceID)
	ctx = ctxkeys.WithSpanID(ctx, spanID)
	return ctx, s
}

// simpleSpan is a lightweight span implementation used by simpleTracer.
type simpleSpan struct {
	mu      sync.Mutex
	traceID string
	spanID  string
	name    string
	status  wrapper.StatusCode
	stDesc  string
	err     error
	attrs   []Attr
}

// SetAttributes records key-value pairs on the span.
func (s *simpleSpan) SetAttributes(attrs ...Attr) {
	if len(attrs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, attrs...)
}

// RecordError stores the most recent error attached to the span.
func (s *simpleSpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// SetStatus updates the span's terminal status.
func (s *simpleSpan) SetStatus(code wrapper.StatusCode, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
	s.stDesc = description
}

// End completes the span — a no-op for the stdlib simpleSpan.
func (s *simpleSpan) End() {}

// SetName updates the span's display name. Implementing SpanRenamer keeps
// two-phase rename (initial "{method} {path}" → "{method} {routePattern}")
// working for HTTP middleware that was written before kernel/wrapper.
func (s *simpleSpan) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

// TraceID returns the trace identifier.
func (s *simpleSpan) TraceID() string { return s.traceID }

// SpanID returns the span identifier.
func (s *simpleSpan) SpanID() string { return s.spanID }

// generateID creates a random hex-encoded ID of the given byte length.
func generateID(byteLen int) string {
	buf := make([]byte, byteLen)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
