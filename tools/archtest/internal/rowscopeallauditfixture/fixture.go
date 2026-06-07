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
//   - MintAll — the RED case: constructs a RowScopeAll obligation OUTSIDE the
//     audited (*auth.Principal).RowVisibility derivation. ROWSCOPEALL-AUDIT-FUNNEL-01
//     MUST flag this.
//   - MintSelf — the GREEN control: a non-all obligation, which MUST NOT be
//     flagged (proves the detector is scope-specific, not "any NewRowVisibility").
package rowscopeallauditfixture

import "github.com/ghbvf/gocell/pkg/tenant"

// MintAll is the RED case (cross-tenant obligation minted outside the audited
// derivation). The funnel archtest must flag this construction.
func MintAll() tenant.RowVisibility {
	v, _ := tenant.NewRowVisibility(tenant.RowScopeAll, "")
	return v
}

// MintSelf is the GREEN control (owner-scoped obligation); it must NOT be flagged.
func MintSelf() tenant.RowVisibility {
	v, _ := tenant.NewRowVisibility(tenant.RowScopeSelf, "subject-x")
	return v
}
