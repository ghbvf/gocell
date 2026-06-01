// Package correlation provides a sealed read-model view of the W0 outbox
// observability envelope for use in issue #1048 (correlate reverse lookup).
//
// Correlation is a sealed read-model view derived solely from the W0 outbox
// observability envelope (kernel/outbox.ObservabilityMetadata). Unexported
// fields make package-external fabrication impossible — upstream single-source
// (Hard) for issue #1048.
//
// The package also exposes Topology, a cell→owner mapping used to look up
// the team and role responsible for a given cell ID. Topology.Owner returns
// (zero, false) on a miss — no silent default owner (fail-closed).
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

// CellOwner describes the responsible team and role for a cell.
// Both fields are exported plain value-object fields with no constraints.
type CellOwner struct {
	Team string
	Role string
}

// Topology maps cell IDs to their owning CellOwner. A nil or empty Topology
// is valid and returns (zero, false) for every query — no silent default owner
// is injected (fail-closed).
type Topology map[string]CellOwner

// Owner returns the CellOwner for the given cellID. If cellID is not in the
// map (including the nil-map and empty-map cases) it returns (CellOwner{},
// false). There is no default fallback owner.
func (t Topology) Owner(cellID string) (CellOwner, bool) {
	owner, ok := t[cellID]
	return owner, ok
}
