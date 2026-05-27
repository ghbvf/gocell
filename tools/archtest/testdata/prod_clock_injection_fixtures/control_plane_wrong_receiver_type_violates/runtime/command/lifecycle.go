// Package command is a RED receiver-type confinement self-check fixture for
// PROD-CLOCK-INJECTION-01.
//
// Blind spot (c) verified: a method in runtime/command/ with a receiver type
// named "otherClock" (NOT "controlPlaneClock") is NOT exempt. The confinement
// check requires the exact receiver type name "controlPlaneClock". Any other
// receiver type in the same package must still be flagged (1 violation).
package command

import "time"

// otherClock is a different struct in the same package. Its methods must NOT
// be exempt from PROD-CLOCK-INJECTION-01 — only methods of controlPlaneClock
// are exempt.
type otherClock struct{}

// newTicker is a method of otherClock, not controlPlaneClock. It lives under
// runtime/command/ but the receiver type check must reject it.
func (otherClock) newTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // must be flagged — receiver type "otherClock" ≠ "controlPlaneClock"
}
