// Package control_plane_marker_wrong_path_violates is a RED P1-3 self-check.
//
// The function carries a perfectly valid doc-comment carve-out marker AND is
// named controlPlaneTicker (an allowlisted name), but it lives at rel
// "usage.go", which is NOT in controlPlaneClockCarveOut. The marker alone must
// NOT exempt it: time.NewTicker is still flagged (1 violation, line 15).
package control_plane_marker_wrong_path_violates

import "time"

// controlPlaneTicker has the right name + marker but the wrong path.
//
//archtest:allow:clock-injection:control-plane control-plane scheduling ticker must use real time. Hard upgrade: backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
func controlPlaneTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // must be flagged — rel not allowlisted
}
