// Package command is a RED blind-spot self-check fixture for
// PROD-CLOCK-INJECTION-01.
//
// This fixture verifies that a time.* call inside a closure (FuncLit) within
// an exempt controlPlaneClock method is still flagged. The carve-out applies
// only to code that is directly inside the method body — NOT to nested FuncLits.
// 1 violation expected (the time.NewTicker call inside the returned closure).
package command

import "time"

// controlPlaneClock is the sealed type whose methods are exempt.
type controlPlaneClock struct{}

// newTickerWithCallback is an exempt controlPlaneClock method that returns a
// closure. The direct body of this method is exempt, but the closure it returns
// is NOT — closures inside an exempt method body are still flagged.
func (controlPlaneClock) newTickerWithCallback(interval time.Duration) func() {
	// This direct call is in the method body — it is exempt (no violation).
	_ = interval.String()
	return func() {
		// This call inside the closure must be flagged (1 violation).
		t := time.NewTicker(interval)
		defer t.Stop()
	}
}
