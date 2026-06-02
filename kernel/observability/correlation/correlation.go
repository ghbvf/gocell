// Package correlation provides a sealed read-model view of the W0 outbox
// observability envelope for use in issue #1048 (correlate trace audit).
//
// Correlation is a sealed read-model view derived solely from the W0 outbox
// observability envelope (kernel/outbox.ObservabilityMetadata). Unexported
// fields make package-external fabrication impossible — upstream single-source
// (Hard) for issue #1048.
//
// Import cycle note: kernel/outbox imports kernel/observability/metrics but
// NOT kernel/observability/correlation; the two subpackages are independent
// Go packages, so importing kernel/outbox here is cycle-free. This was
// verified with `go build ./kernel/observability/correlation/` after adding a
// trivial import of kernel/outbox (build succeeded, no cycle).
package correlation

import (
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Correlation is a sealed read-model carrying the three cross-cutting
// observability IDs extracted from the W0 outbox observability envelope.
//
// All fields are unexported. The only way to obtain a non-zero Correlation
// outside this package is via FromObservability. Package-external struct
// literals cannot set the unexported fields, making fabrication structurally
// impossible (Hard sealed construction).
type Correlation struct {
	traceID       string
	requestID     string
	correlationID string
}

// FromObservability constructs a Correlation from the W0 outbox observability
// envelope. It is the single derivation path — there is no other constructor.
// TraceParent is intentionally not carried: Correlation represents the three
// cross-cutting opaque IDs, not the full W3C propagation context.
func FromObservability(meta outbox.ObservabilityMetadata) Correlation {
	return Correlation{
		traceID:       string(meta.TraceID),
		requestID:     string(meta.RequestID),
		correlationID: string(meta.CorrelationID),
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
