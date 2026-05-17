// Package control_plane_exempt_func_closure_violates is a RED blind-spot
// self-check fixture for PROD-CLOCK-INJECTION-01.
//
// This fixture verifies that a time.* call inside a closure (FuncLit) within
// an otherwise-exempt FuncDecl is still flagged. The carve-out applies only to
// code that is directly inside the marked FuncDecl body — NOT to nested
// FuncLits. 1 violation expected (declared via spec.Violation()).
package control_plane_exempt_func_closure_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

// exemptFuncWithClosure is properly marked and returns a closure.
// The closure itself is NOT exempt — only direct code in the FuncDecl body is.
//
//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1
func exemptFuncWithClosure(interval time.Duration) func() {
	// This direct call is exempt (directly inside the marked FuncDecl body).
	_ = interval.String()
	return func() {
		// This call inside the closure must be flagged (1 violation).
		spec.Violation()
		t := time.NewTicker(interval)
		defer t.Stop()
	}
}
