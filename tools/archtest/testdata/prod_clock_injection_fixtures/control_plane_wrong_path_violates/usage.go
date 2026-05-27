// Package control_plane_wrong_path_violates is a RED receiver-type confinement
// self-check fixture for PROD-CLOCK-INJECTION-01.
//
// Blind spot (b) verified: a struct named "controlPlaneClock" with a method
// calling time.NewTicker is NOT exempt when the file's module-relative path is
// NOT under "runtime/command/". The path gate (rel prefix check) must fire, and
// time.NewTicker must be flagged (1 violation).
package control_plane_wrong_path_violates

import "time"

// controlPlaneClock has the right receiver type name but the wrong file path.
// The path gate prevents exemption — any package outside runtime/command/ is
// forbidden from using stdlib time.* regardless of struct name.
type controlPlaneClock struct{}

// newTicker is a method of controlPlaneClock but lives outside runtime/command/.
// It must be flagged.
func (controlPlaneClock) newTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // must be flagged — rel not under runtime/command/
}
