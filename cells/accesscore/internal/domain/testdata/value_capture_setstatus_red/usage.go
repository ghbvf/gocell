// Package value_capture_setstatus_red is a RED fixture for the
// AUTHZ-MUTATION-APPLY-FUNNEL-01 form-completeness invariant (PR #1196
// round-2): non-allowlisted code captures domain.User.SetStatus as a
// function value WITHOUT immediately invoking it. The scanner must detect
// this regression — function-value capture is the bypass form a
// CallExpr.Fun-only walk would miss.
//
// LOCATION RATIONALE: imports cells/accesscore/internal/domain so Go's
// internal-import rule requires this fixture to live under cells/accesscore/;
// `testdata/` excludes the package from `go build ./...` while archtest
// loads it via an explicit packages.Load pattern.
package value_capture_setstatus_red

import (
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// badValueCapture stores domain.User.SetStatus as a function value. The
// stored value is NOT invoked inside badValueCapture itself — the deferred
// call would happen at the call site of fn. A CallExpr.Fun-only scanner
// would miss this; the form-complete SelectorExpr scanner catches it
// because info.Selections records a MethodVal selection for the bare
// `u.SetStatus` SelectorExpr.
func badValueCapture(u *domain.User) func(domain.UserStatus, time.Time) {
	fn := u.SetStatus
	return fn
}

// badReturnDirect returns the method value directly (no intermediate var)
// — same SelectorExpr resolution, different surrounding statement.
func badReturnDirect(u *domain.User) func(bool, time.Time) {
	return u.SetPasswordResetRequired
}

// badArgPass passes the method value as an argument to another function.
func badArgPass(u *domain.User) {
	consume(u.SetStatus)
}

func consume(_ func(domain.UserStatus, time.Time)) {}
