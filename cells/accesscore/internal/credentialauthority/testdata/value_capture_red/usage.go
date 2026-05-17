// Package value_capture_red is a RED fixture for
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (value-capture prong, P2-B). It
// captures credentialauthority.Assert and domain.(*User).CanAuthenticate
// as function values in all three forms the archtest must detect:
//   - AssignStmt RHS:  fn := pkg.Func
//   - ValueSpec value: var fn = pkg.Func
//   - CallExpr arg:    helper(pkg.Func)
//
// Each form must produce at least one violation (per-bucket counting).
//
// LOCATION RATIONALE: the fixture imports
// cells/accesscore/internal/credentialauthority + .../internal/domain,
// so Go's internal-import rule requires this fixture to live under
// cells/accesscore/. The `testdata/` directory excludes the package
// from `go build ./...` while archtest loads it via an explicit
// packages.Load pattern.
package value_capture_red

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// invokeFn is a sink that takes any function value — used to force a
// CallExpr-arg form of value capture.
func invokeFn(_ func(*domain.User, ...credentialauthority.Check) error) {}

// invokeBool is a sink for method values returning bool — used for the
// CanAuthenticate CallExpr-arg form.
func invokeBool(_ func() bool) {}

// badAssignAssert captures credentialauthority.Assert as a function value
// via AssignStmt. The archtest must flag this.
func badAssignAssert(u *domain.User) error {
	fn := credentialauthority.Assert
	return fn(u)
}

// badValueSpecCanAuth captures CanAuthenticate as a method value via
// ValueSpec (top-level var). The archtest must flag this.
//
// We use a package-level var so it is unambiguously a ValueSpec rather
// than an AssignStmt; the function returns the captured value so the
// compiler does not optimize it away.
var canAuthCapture = (&domain.User{}).CanAuthenticate

// reachCanAuthCapture keeps the package-level var live (preventing dead-
// code elimination); the archtest cares about the ValueSpec form, not the
// runtime call.
func reachCanAuthCapture() bool {
	return canAuthCapture()
}

// badCallArgAssert passes credentialauthority.Assert as a call argument.
// The archtest must flag this.
func badCallArgAssert() {
	invokeFn(credentialauthority.Assert)
}

// badCallArgCanAuth passes a CanAuthenticate method value as a call
// argument. The archtest must flag this.
func badCallArgCanAuth(u *domain.User) {
	invokeBool(u.CanAuthenticate)
}
