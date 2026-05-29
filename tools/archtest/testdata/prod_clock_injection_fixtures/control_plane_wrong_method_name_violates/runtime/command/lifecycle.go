// Package command is a RED fixture for PROD-CLOCK-INJECTION-01 #1136 review F2.
//
// It asserts that the host-scoped carve-out rejects a new controlPlaneClock
// method whose name is in NO host's set: the receiver-type-only form would have
// wrongly exempted any time.* call in any controlPlaneClock method body. Under
// the tightened rule, `harvest()` — not in the runtime/command set {newTicker,
// newProbeTimer} — gets no carve-out and the time.Sleep call inside is flagged.
package command

import "time"

type controlPlaneClock struct{}

// newTicker is the legitimate sanctioned pair (newTicker, NewTicker).
func (controlPlaneClock) newTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // exempt (sanctioned pair)
}

// harvest is a NEW method on controlPlaneClock — in no host's carve-out set.
// Any time.* call here MUST be flagged (the receiver-type-only form would have
// exempted it; the host-scoped table rejects it).
func (controlPlaneClock) harvest(d time.Duration) {
	time.Sleep(d) // RED: harvest is in no host carve-out set
}
