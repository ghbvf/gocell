//go:build archtest_fixture

// Package authzclockfixture is a RED fixture for AUTHZ-EVAL-CLOCK-INJECTED-01.
// It reads the wall clock via time.Now(), which the ABAC engine must never do —
// the detector must flag this call. Gated behind the archtest_fixture build tag
// so it is invisible to the Production() scan and loaded only by the Fixture()
// façade in the reverse self-check.
package authzclockfixture

import "time"

// WallClockHour reads the wall clock directly (RED): the engine must instead use
// the injected clock.Clock.
func WallClockHour() int {
	return time.Now().Hour()
}
