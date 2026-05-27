// Package control_plane_closure_violates is a RED reverse self-check fixture
// for PROD-CLOCK-INJECTION-01.
//
// This fixture verifies that a non-exempt function (not a controlPlaneClock
// method) containing a closure that calls time.NewTicker is still flagged.
// 1 violation expected (nonExemptFunc, via the closure).
package control_plane_closure_violates

import "time"

// nonExemptFunc is a free function (no receiver, not a controlPlaneClock method).
// Its closure calls time.NewTicker — must be flagged.
func nonExemptFunc(interval time.Duration) func() {
	return func() {
		t := time.NewTicker(interval) // must be flagged (1 violation)
		defer t.Stop()
	}
}
