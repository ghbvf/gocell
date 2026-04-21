// Package configpublish — PublishFailureMode type.
//
// S10 MODE-SEMANTIC-SPLIT-01: separates write-path publisher failure semantics
// from read-path cursor tolerance (query.RunMode). Defined here — not in
// pkg/query — because it is a configpublish-specific concern. If audit-core or
// access-core need the same pattern later, promote to a shared location.
//
// ref: Uber fx — each decision gets its own typed injection boundary.
// ref: Kubernetes — orthogonal typed policy enums.
package configpublish

// PublishFailureMode controls whether direct publisher failures on write paths
// are propagated (fail-closed) or tolerated (fail-open) when no outbox writer
// is configured.
//
// Zero value is fail-closed — the safe production default. Demo assemblies
// using DiscardPublisher set fail-open so the write path does not block.
type PublishFailureMode uint8

const (
	// PublishFailureModeFailClosed propagates publisher failures as errors.
	// This is the zero value and therefore the safe default for production.
	PublishFailureModeFailClosed PublishFailureMode = iota

	// PublishFailureModeFailOpen swallows publisher failures after a warning
	// log, allowing the write path to succeed even when the publisher is
	// degraded. Intended for demo mode only.
	PublishFailureModeFailOpen
)

// IsFailOpen reports whether publisher failures are tolerated.
func (m PublishFailureMode) IsFailOpen() bool { return m == PublishFailureModeFailOpen }

// String returns a stable lowercase label suitable for structured logs and
// metrics. Unknown values return "unknown" to prevent silent misuse.
func (m PublishFailureMode) String() string {
	switch m {
	case PublishFailureModeFailClosed:
		return "fail-closed"
	case PublishFailureModeFailOpen:
		return "fail-open"
	default:
		return "unknown"
	}
}

// PublishFailureModeForDemo maps demo=true to fail-open and all other modes to
// fail-closed. Called once at Cell Init() time; do not call per-request.
func PublishFailureModeForDemo(demo bool) PublishFailureMode {
	if demo {
		return PublishFailureModeFailOpen
	}
	return PublishFailureModeFailClosed
}
