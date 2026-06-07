//go:build archtest_fixture

// Package rowscopeallauditfixture provides a deliberate RED fixture for the
// ROWSCOPEALL-AUDIT-FUNNEL-01 archtest. Loaded only under the archtest_fixture
// build tag (the tag literal must agree with the unexported fixtureBuildTag
// const in tools/archtest/fixture.go; Go build directives cannot reference a Go
// const). The build tag excludes this package from `go build ./...` /
// `go test ./...`, so it never pollutes real-repo scans.
//
// # Cases covered
//
// The funnel is FAIL-CLOSED (#1759 F1): a production tenant.NewRowVisibility
// call is flagged when its scope argument is RowScopeAll OR cannot be proven to
// be a non-All compile-time constant. The RED cases below exercise every
// laundering form; the GREEN controls prove the detector does not over-flag a
// constant it CAN prove is non-All.
//
// RED (MUST be flagged):
//   - MintAll — direct tenant.RowScopeAll const, outside the audited derivation.
//   - MintAllViaLocalVar — scope laundered through a local VAR; not a
//     compile-time constant, so the scanner cannot prove it is non-All →
//     fail-closed (the F1 blind spot the old direct-const detector missed).
//   - MintAllViaLocalConst — scope laundered through a local CONST whose value
//     folds to RowScopeAll; caught by constant folding.
//
// GREEN (MUST NOT be flagged):
//   - MintSelf — direct non-all const (proves the detector is scope-specific,
//     not "any NewRowVisibility").
//   - MintSelfViaLocalConst — local CONST whose value folds to a non-All scope
//     (proves fail-closed does not over-flag a provable non-All constant).
package rowscopeallauditfixture

import "github.com/ghbvf/gocell/pkg/tenant"

// MintAll is the RED case (cross-tenant obligation minted outside the audited
// derivation). The funnel archtest must flag this construction.
func MintAll() tenant.RowVisibility {
	v, _ := tenant.NewRowVisibility(tenant.RowScopeAll, "")
	return v
}

// MintAllViaLocalVar is a RED case: the scope is laundered through a local var,
// so it is NOT a compile-time constant the scanner can prove is non-All. The
// fail-closed detector MUST flag it (this is the F1 blind spot that the old
// ResolvePackageRef-only detector silently passed).
func MintAllViaLocalVar() tenant.RowVisibility {
	s := tenant.RowScopeAll
	v, _ := tenant.NewRowVisibility(s, "")
	return v
}

// MintAllViaLocalConst is a RED case: the scope is laundered through a local
// CONST whose value folds to RowScopeAll. The detector MUST flag it (go/types
// constant folding resolves the value to All).
func MintAllViaLocalConst() tenant.RowVisibility {
	const s = tenant.RowScopeAll
	v, _ := tenant.NewRowVisibility(s, "")
	return v
}

// MintSelf is a GREEN control (direct owner-scoped const); it must NOT be flagged.
func MintSelf() tenant.RowVisibility {
	v, _ := tenant.NewRowVisibility(tenant.RowScopeSelf, "subject-x")
	return v
}

// MintSelfViaLocalConst is a GREEN control: a local CONST whose value folds to a
// non-All scope. It proves the fail-closed detector does NOT over-flag a
// constant it can prove is non-All (otherwise every laundered-but-safe const
// would be a false positive).
func MintSelfViaLocalConst() tenant.RowVisibility {
	const s = tenant.RowScopeSelf
	v, _ := tenant.NewRowVisibility(s, "subject-y")
	return v
}
