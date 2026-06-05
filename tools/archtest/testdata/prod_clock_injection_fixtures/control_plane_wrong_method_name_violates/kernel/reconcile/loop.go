// Package reconcile is a RED fixture for PROD-CLOCK-INJECTION-01 #1136 review F2.
//
// It asserts that the host-scoped carve-out rejects a controlPlaneClock method
// whose name is in NO host's set: the receiver-type-only form would have wrongly
// exempted any time.* call in any controlPlaneClock method body. Under the
// tightened rule, `harvest()` — not in the kernel/reconcile set {newProbeTimer,
// newRequeueTimer, newRenewTicker, now} — gets no carve-out and the time.Sleep
// call inside is flagged. The legitimate newProbeTimer pair stays exempt.
package reconcile

import "time"

type controlPlaneClock struct{}

// newProbeTimer is the legitimate sanctioned pair (newProbeTimer, NewTimer).
func (controlPlaneClock) newProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d) // exempt (sanctioned pair)
}

// harvest is a NEW method on controlPlaneClock — in no host's carve-out set.
// Any time.* call here MUST be flagged.
func (controlPlaneClock) harvest(d time.Duration) {
	time.Sleep(d) // RED: harvest is in no host carve-out set
}
