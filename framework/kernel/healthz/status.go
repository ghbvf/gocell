package healthz

// Status is a three-valued readiness probe outcome.
//
// Severity ordering: Up(0) < Degraded(1) < Down(2). Timeout and panic are
// classified as Down by the aggregator implementation; they do not have
// distinct Status values at this layer (the HTTP transport may translate
// them to "timeout" / "panic" wire strings when verbose output is requested).
//
// ref: spring-projects/spring-boot StatusAggregator — severity priority order
// ref: kubernetes/kubernetes apiserver healthz — binary error/nil collapsed
// into a tri-state by GoCell for fail-open degraded semantics.
type Status uint8

const (
	// StatusUp signals the probe completed successfully.
	StatusUp Status = iota
	// StatusDegraded signals the probe completed but the underlying dependency
	// is degraded (fail-open). HTTP transports map this to 200, not 503.
	StatusDegraded
	// StatusDown signals the probe failed (error, timeout, or panic). HTTP
	// transports map this to 503.
	StatusDown
)

// String returns the canonical lower-case label for a Status value. Unknown
// values map to "unknown".
func (s Status) String() string {
	switch s {
	case StatusUp:
		return "up"
	case StatusDegraded:
		return "degraded"
	case StatusDown:
		return "down"
	default:
		return "unknown"
	}
}

// WorseStatus returns the more severe of two Status values. Used to fold a
// set of per-probe results into an aggregate Snapshot.Overall.
func WorseStatus(a, b Status) Status {
	if a > b {
		return a
	}
	return b
}
