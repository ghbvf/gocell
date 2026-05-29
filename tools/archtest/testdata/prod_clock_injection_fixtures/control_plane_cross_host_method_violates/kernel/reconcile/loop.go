// Package reconcile is a RED fixture for PROD-CLOCK-INJECTION-01 #1275 review F1.
//
// It asserts that the host-scoped carve-out (controlPlaneClockCarveOut) rejects
// a host using ANOTHER host's sanctioned method. "newTicker" is sanctioned only
// for runtime/command; the former host-agnostic method map would have exempted
// it in any host. Here kernel/reconcile declares controlPlaneClock.newTicker —
// not in the kernel/reconcile set {newProbeTimer, newRequeueTimer, now} — so the
// NewTicker call inside is flagged. The legitimate newProbeTimer pair stays
// exempt (control case).
package reconcile

import "time"

type controlPlaneClock struct{}

// newProbeTimer is in the kernel/reconcile set — exempt (control case).
func (controlPlaneClock) newProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d) // exempt (sanctioned for kernel/reconcile)
}

// newTicker belongs to runtime/command, NOT kernel/reconcile. The host-scoped
// table denies cross-host borrowing, so this NewTicker call is flagged.
func (controlPlaneClock) newTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // RED: newTicker is not a kernel/reconcile pair
}
