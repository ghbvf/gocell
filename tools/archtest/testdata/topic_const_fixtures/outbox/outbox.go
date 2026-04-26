// Package outbox provides a minimal stub of kernel/outbox for archtest
// fixture testing. Only the fields and constants exercised by the
// OUTBOX-TOPIC-FAILOPEN-01 scanner are reproduced here.
package outbox

// Entry is the minimal stub of kernel/outbox.Entry sufficient for topic-
// const resolver fixture tests.
type Entry struct {
	ID            string
	Topic         string
	EventType     string
	FailurePolicy FailurePolicy
}

// FailurePolicy controls per-entry publisher failure behaviour.
type FailurePolicy int

const (
	// FailurePolicyDefault is the zero value; falls through to Emitter default.
	FailurePolicyDefault FailurePolicy = iota
	// FailurePolicyFailOpen drops on publisher failure.
	FailurePolicyFailOpen
	// FailurePolicyFailClosed surfaces publisher failure to caller.
	FailurePolicyFailClosed
)
