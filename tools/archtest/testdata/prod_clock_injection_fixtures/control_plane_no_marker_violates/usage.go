// Package control_plane_no_marker_violates is a RED reverse self-check fixture
// for PROD-CLOCK-INJECTION-01.
//
// A free function (no receiver) in a path other than runtime/command/ that
// calls time.NewTicker must be flagged. This verifies that only methods of
// controlPlaneClock inside runtime/command/ are exempt — a plain free function
// with the same logic is still a violation (1 violation expected).
package control_plane_no_marker_violates

import "time"

// plainFunc is a free function (no receiver) calling time.NewTicker.
// It must be flagged — the carve-out only applies to controlPlaneClock methods
// inside runtime/command/, not to free functions anywhere.
func plainFunc(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // must be flagged — free function, not a controlPlaneClock method
}
