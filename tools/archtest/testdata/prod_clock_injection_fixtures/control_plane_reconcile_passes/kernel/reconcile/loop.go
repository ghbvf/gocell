// Package reconcile is the GREEN control-plane receiver-type confinement fixture
// for the kernel/reconcile host of PROD-CLOCK-INJECTION-01
// (RECONCILE-LOOP-CLOCK-CARVEOUT-01). It mirrors the real kernel/reconcile/ path
// so the rel-gate (controlPlaneClockCarveOut includes "kernel/reconcile/") plus
// receiver type name "controlPlaneClock" plus the host-scoped (method, callee)
// pairs newProbeTimer→NewTimer / newRequeueTimer→NewTimer / now→Now yield 0
// violations.
package reconcile

import "time"

// controlPlaneClock is the sealed, real-only clock for control-plane scheduling.
// It is an empty package-private struct: no package outside kernel/reconcile can
// name it. Its methods are the sole sanctioned callsites for stdlib
// time.NewTimer / time.Now in this package.
type controlPlaneClock struct{}

// newProbeTimer is exempt: controlPlaneClock method in kernel/reconcile/ whose
// (method, callee) pair newProbeTimer→NewTimer is registered.
func (controlPlaneClock) newProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d) // exempt
}

// newRequeueTimer is exempt: pair newRequeueTimer→NewTimer is registered.
func (controlPlaneClock) newRequeueTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d) // exempt
}

// now is exempt: pair now→Now is registered (reconcile-duration measurement).
func (controlPlaneClock) now() time.Time {
	return time.Now() // exempt
}

// cleanFunc uses time.Duration only as a type — no forbidden time.* call.
func cleanFunc(d time.Duration) time.Duration {
	return d * 2
}
