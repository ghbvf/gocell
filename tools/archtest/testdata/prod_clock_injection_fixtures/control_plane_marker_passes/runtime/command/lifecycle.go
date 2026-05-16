// Package command is the GREEN control-plane carve-out fixture. It mirrors the
// real runtime/command/lifecycle.go path + func names so the (rel, name)
// allowlist (controlPlaneClockCarveOut) plus a valid doc-comment marker yields
// 0 violations — the only shape the carve-out accepts (review P1-3).
package command

import "time"

// controlPlaneTicker creates a real-time ticker.
//
//archtest:allow:clock-injection:control-plane control-plane scheduling ticker must use real time; injecting a fake clock reintroduces startup-deadlock regression. Hard upgrade: backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
func controlPlaneTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval)
}

// controlPlaneProbeTimer creates a real-time timer for the probe window.
//
//archtest:allow:clock-injection:control-plane control-plane probe timer must use real time; same rationale as controlPlaneTicker. Hard upgrade: backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
func controlPlaneProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d)
}

// cleanFunc does not call any forbidden time.* symbols; it only uses time.Duration as a type.
func cleanFunc(d time.Duration) time.Duration {
	return d * 2
}
