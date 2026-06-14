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
// As of #1760 the sole producer of a RowScopeAll obligation is the sealed
// tenant.NewCrossTenantVisibility (tenant.NewRowVisibility REJECTS RowScopeAll).
// The funnel scan therefore flags any production call to NewCrossTenantVisibility
// outside the allowlist. Because that constructor takes NO scope argument, the
// old scope-laundering forms (var/const aliasing) are structurally impossible —
// one RED case is sufficient.
//
// RED (MUST be flagged):
//   - MintCrossTenantOutsideFunnel — calls tenant.NewCrossTenantVisibility
//     outside the audited derivation.
//
// GREEN (MUST NOT be flagged):
//   - MintSelf — calls tenant.NewRowVisibility(RowScopeSelf, …); proves the scan
//     is symbol-specific (targets NewCrossTenantVisibility, not "any RowVisibility
//     construction"), so the general constructor is never over-flagged.
package rowscopeallauditfixture

import "github.com/ghbvf/gocell/framework/pkg/tenant"

// MintCrossTenantOutsideFunnel is the RED case: a cross-tenant obligation minted
// outside the audited super-admin derivation. The funnel archtest must flag this
// construction.
func MintCrossTenantOutsideFunnel() tenant.RowVisibility {
	return tenant.NewCrossTenantVisibility().Visibility()
}

// MintSelf is a GREEN control: it constructs a non-All obligation via the general
// constructor. It must NOT be flagged — the scan targets NewCrossTenantVisibility,
// not every RowVisibility construction.
func MintSelf() tenant.RowVisibility {
	v, _ := tenant.NewRowVisibility(tenant.RowScopeSelf, "subject-x")
	return v
}
