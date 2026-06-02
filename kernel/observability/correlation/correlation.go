// Package correlation provides a sealed read-model carrying the three
// cross-cutting observability IDs (trace, request, correlation) for use in
// issue #1048 (trace → audit reverse lookup).
//
// Correlation is a sealed value type: its fields are unexported, so the only
// way to obtain a non-zero Correlation outside this package is via New.
// Package-external struct literals cannot set the unexported fields, making
// fabrication structurally impossible (Hard sealed construction, upstream
// single-source for #1048).
//
// Layering note (KERNEL-INTERNAL-DAG-01): kernel/observability is a leaf in
// the kernel-internal DAG — kernel/outbox imports kernel/observability/metrics
// for its metrics provider, so the reverse edge (observability → outbox) is a
// forbidden cycle. This package therefore does NOT import kernel/outbox; it
// takes the three IDs as plain strings. The bridge from the W0 outbox
// observability envelope (kernel/outbox.ObservabilityMetadata) lives at the
// sole consumer — the audit appender (cells/auditcore/internal/appender) —
// which holds both the outbox.Entry and this read-model. The seal (unexported
// fields + single constructor) is preserved regardless of where the bridge
// lives; trust in the IDs is positional (the appender feeds
// entry.Observability() values, which originate from the sealed outbox.Entry).
package correlation

// Correlation is a sealed read-model carrying the three cross-cutting
// observability IDs.
//
// All fields are unexported. The only way to obtain a non-zero Correlation
// outside this package is via New. Package-external struct literals cannot set
// the unexported fields, making fabrication structurally impossible (Hard
// sealed construction).
type Correlation struct {
	traceID       string
	requestID     string
	correlationID string
}

// New constructs a Correlation from the three cross-cutting observability IDs.
// It is the single construction path — there is no other constructor.
// TraceParent is intentionally not carried: Correlation represents the three
// opaque IDs, not the full W3C propagation context. Callers bridging from the
// W0 outbox observability envelope pass string(meta.TraceID), string(meta.RequestID)
// and string(meta.CorrelationID).
func New(traceID, requestID, correlationID string) Correlation {
	return Correlation{
		traceID:       traceID,
		requestID:     requestID,
		correlationID: correlationID,
	}
}

// TraceID returns the trace ID. Returns empty string for a zero-value
// Correlation.
func (c Correlation) TraceID() string { return c.traceID }

// RequestID returns the request ID. Returns empty string for a zero-value
// Correlation.
func (c Correlation) RequestID() string { return c.requestID }

// CorrelationID returns the correlation ID. Returns empty string for a
// zero-value Correlation.
func (c Correlation) CorrelationID() string { return c.correlationID }
