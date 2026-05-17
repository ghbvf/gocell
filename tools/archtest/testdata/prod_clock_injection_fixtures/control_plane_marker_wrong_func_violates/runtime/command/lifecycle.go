// Package command is a RED P1-3 self-check: a THIRD marked function added to
// the allowlisted path runtime/command/lifecycle.go. controlPlaneClockCarveOut
// lists only controlPlaneTicker / controlPlaneProbeTimer for this path, so a
// new marked helper must NOT be exempt — time.NewTicker is still flagged,
// even though both the path and the marker are valid.
// 1 violation expected (declared via spec.Violation()).
package command

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

// controlPlaneSweepHelper is a third, NON-allowlisted function on the allowed
// path. The valid marker must NOT exempt it (only the two listed names are).
//
//archtest:allow:clock-injection:control-plane attempting to self-exempt a third function on the allowlisted path. Hard upgrade: backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
func controlPlaneSweepHelper(interval time.Duration) *time.Ticker {
	spec.Violation()
	return time.NewTicker(interval) // must be flagged — name not in allowlist
}
