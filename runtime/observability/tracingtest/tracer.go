// Package tracingtest provides an in-process, stdlib-only Tracer fixture for
// tests. It is intentionally test-only: production code wires the OTel adapter
// (adapters/otel) via bootstrap.WithTracer, falling back to
// kernel/wrapper.NoopTracer{} when no tracer is configured.
//
// # Deliberate propagation asymmetry (B2-A-20)
//
// simpleTracer is in-process-only by design:
//
//   - INBOUND (symmetric, intentional): Start reuses an upstream trace ID that
//     runtime/http/middleware.trace_propagation already extracted from W3C/B3
//     headers into kernel/pkg/ctxkeys. The OTel adapter honors inbound context
//     the same way, so this side is symmetric and correct.
//   - OUTBOUND (asymmetric, intentional): simpleTracer NEVER injects a W3C
//     `traceparent` header onto outbound calls. Cross-process trace propagation
//     is exclusively the responsibility of the OTel adapter (adapters/otel,
//     OpenTelemetry SDK + propagation.TraceContext).
//
// This asymmetry is the reason the fixture must never reach production: a
// service wired with simpleTracer would silently drop cross-process trace
// links. The boundary is enforced by archtest
// TRACING-SIMPLETRACER-TEST-ONLY-01 (R-B bans non-_test.go imports of this
// package; R-A seals the deleted runtime/observability/tracing.NewTracer).
//
// ref: go.opentelemetry.io/otel — Tracer/Span API, W3C TraceContext
// propagation (outbound injection lives in adapters/otel).
//
// INVARIANT: TRACING-SIMPLETRACER-TEST-ONLY-01
package tracingtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// Compile-time assertion: simpleSpan implements the kernel Span interface.
var _ wrapper.Span = (*simpleSpan)(nil)

// simpleTracer is a stdlib-only Tracer fixture for tests. It generates random
// trace/span IDs and reuses an inbound trace ID from ctxkeys. It does NOT
// inject outbound W3C traceparent — see package doc.
type simpleTracer struct {
	name string
}

// NewSimpleTracer creates an in-process test Tracer that generates random
// trace/span IDs and reuses any inbound trace ID already in context. For
// OpenTelemetry / cross-process propagation, use adapters/otel in production.
func NewSimpleTracer(name string) wrapper.Tracer {
	return &simpleTracer{name: name}
}

func (t *simpleTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
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
	attrs   []wrapper.Attr
}

// SetAttributes records key-value pairs on the span.
func (s *simpleSpan) SetAttributes(attrs ...wrapper.Attr) {
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

// SetName updates the span's display name. Implementing wrapper.SpanRenamer
// keeps two-phase rename ("{method} {path}" → "{method} {routePattern}")
// working for HTTP middleware tests.
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
