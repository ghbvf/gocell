// Package control_plane_closure_violates is a RED reverse self-check fixture
// for PROD-CLOCK-INJECTION-01.
//
// This fixture verifies that a non-exempt function (no doc-comment marker)
// containing a closure that calls time.NewTicker is still flagged. The presence
// of an exempt function in the same file does NOT grant file-level exemption.
// 1 violation expected (nonExemptFunc, via the closure).
package control_plane_closure_violates

import "time"

// exemptFunc is properly marked and does NOT itself call any time.* symbols.
//
//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1
func exemptFunc(interval time.Duration) string {
	return interval.String()
}

// nonExemptFunc has no marker. Its closure calls time.NewTicker — must be flagged.
func nonExemptFunc(interval time.Duration) func() {
	return func() {
		t := time.NewTicker(interval) // must be flagged (1 violation)
		defer t.Stop()
	}
}
