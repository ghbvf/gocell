// Package command is a RED P1-3 self-check: a THIRD marked function added to
// the allowlisted path runtime/command/lifecycle.go. controlPlaneClockCarveOut
// lists only controlPlaneTicker / controlPlaneProbeTimer for this path, so a
// new marked helper must NOT be exempt — time.NewTicker is still flagged
// (1 violation), even though both the path and the marker are valid.
package command

import "time"

// controlPlaneSweepHelper is a third, NON-allowlisted function on the allowed
// path. The valid marker must NOT exempt it (only the two listed names are).
//
//archtest:allow:clock-injection:control-plane attempting to self-exempt a third function on the allowlisted path. Hard upgrade: backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
func controlPlaneSweepHelper(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval) // must be flagged — name not in allowlist
}
