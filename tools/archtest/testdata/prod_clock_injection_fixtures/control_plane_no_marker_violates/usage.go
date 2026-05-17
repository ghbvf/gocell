// Package control_plane_no_marker_violates is a RED reverse self-check fixture
// for PROD-CLOCK-INJECTION-01.
//
// Blind spot verified: placing //archtest:allow:clock-injection:control-plane
// in a non-doc inline comment inside the function body is NOT recognized as a
// carve-out. The function is still flagged.
// 1 violation expected (declared via spec.Violation()).
package control_plane_no_marker_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

// wrongPlaceFunc has the marker in an inline comment inside the body, not in
// the doc comment group. The marker is therefore ignored by
// clockControlPlaneAllowedFuncs, which only scans fd.Doc.
func wrongPlaceFunc(interval time.Duration) *time.Ticker {
	//archtest:allow:clock-injection:control-plane this inline comment is NOT a doc comment and must NOT exempt this function
	spec.Violation()
	return time.NewTicker(interval) // must be flagged
}
