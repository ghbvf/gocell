// Package var_init_setstatus_red is a RED fixture for the
// AUTHZ-MUTATION-APPLY-FUNNEL-01 archtest blind-spot §6 (package-level
// var init bypass).
//
// It invokes domain.User.SetStatus from a package-level var-init expression
// — a node that has NO enclosing FuncDecl. The scanner must treat
// ResolveEnclosingFunc → (nil, false) as an automatic violation with the
// "outside any FuncDecl" substring, proving the var-init bypass form is
// covered.
//
// LOCATION RATIONALE: imports corecells/accesscore/internal/domain so Go's
// internal-import rule requires this fixture to live under corecells/accesscore/;
// `testdata/` excludes the package from `go build ./...` while archtest
// loads it via an explicit packages.Load pattern.
package var_init_setstatus_red

import (
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
)

// callSetStatusAtInit returns 0 after invoking the banned setter at
// package-init time. The actual call site is the FuncLit invocation `()`,
// which lives in a var-init expression — no enclosing FuncDecl.
//
//nolint:gochecknoglobals // RED fixture: package-level call is the regression form being asserted
var _ = func() int {
	u := &domain.User{}
	u.SetStatus(domain.StatusLocked, time.Now())
	return 0
}()
