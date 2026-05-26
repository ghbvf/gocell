// Package command is the GREEN control-plane receiver-type confinement fixture.
// It mirrors the real runtime/command/ path so the rel-gate
// (strings.HasPrefix(rel, "runtime/command/")) plus receiver type name
// "controlPlaneClock" yields 0 violations.
//
// The only shape the carve-out accepts: a method whose receiver type is
// controlPlaneClock AND the file's module-relative path is under
// runtime/command/.
package command

import "time"

// controlPlaneClock is the sealed, real-only clock for control-plane scheduling.
// It is an empty package-private struct: no package outside runtime/command can
// name it. Its methods are the sole sanctioned callsites for stdlib
// time.NewTicker / time.NewTimer in this package.
type controlPlaneClock struct{}

// newTicker creates a real-time ticker for control-plane scheduling.
// This is exempt from PROD-CLOCK-INJECTION-01 because it is a method of
// controlPlaneClock AND the file is under runtime/command/.
func (controlPlaneClock) newTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // exempt: controlPlaneClock method in runtime/command/
}

// newProbeTimer creates a real-time timer for the startup probe window.
// Also exempt for the same reason.
func (controlPlaneClock) newProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d) // exempt: controlPlaneClock method in runtime/command/
}

// cleanFunc does not call any forbidden time.* symbols; it only uses
// time.Duration as a type. No receiver, no violation.
func cleanFunc(d time.Duration) time.Duration {
	return d * 2
}
