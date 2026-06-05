// Package reconcile is a RED receiver-type confinement self-check fixture for
// PROD-CLOCK-INJECTION-01.
//
// Blind spot (c) verified: a method in kernel/reconcile/ with a receiver type
// named "otherClock" (NOT "controlPlaneClock") is NOT exempt. The confinement
// check requires the exact receiver type name "controlPlaneClock". Any other
// receiver type in the same host package must still be flagged (1 violation).
package reconcile

import "time"

// otherClock is a different struct in the same package. Its methods must NOT
// be exempt from PROD-CLOCK-INJECTION-01 — only methods of controlPlaneClock
// are exempt.
type otherClock struct{}

// newRenewTicker is a method of otherClock, not controlPlaneClock. It lives under
// kernel/reconcile/ but the receiver type check must reject it.
func (otherClock) newRenewTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // must be flagged — receiver type "otherClock" ≠ "controlPlaneClock"
}
