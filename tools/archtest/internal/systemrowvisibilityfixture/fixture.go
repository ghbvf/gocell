//go:build archtest_fixture

// Package systemrowvisibilityfixture provides a deliberate RED fixture for the
// SYSTEM-ROWVISIBILITY-CALLSITE-01 archtest. Loaded only under the
// archtest_fixture build tag (the tag literal must agree with the unexported
// fixtureBuildTag const in tools/archtest/fixture.go; Go build directives cannot
// reference a Go const). The build tag excludes this package from
// `go build ./...` / `go test ./...`, so it never pollutes real-repo scans.
//
// # Cases covered
//
// SystemRowVisibility() mints the tenant-wide (no owner predicate) obligation. On
// a subject-facing read path that is a footgun: it silently removes the owner
// predicate and leaks every row in the tenant. The funnel scan flags any
// production call to tenant.SystemRowVisibility outside the sanctioned-caller
// allowlist.
//
// RED (MUST be flagged):
//   - CallSystemRowVisibilityOutsideAllowlist — calls tenant.SystemRowVisibility
//     from an un-allowlisted file (the stand-in for a subject-facing handler that
//     wrongly bypasses the principal-derived obligation).
//
// GREEN (MUST NOT be flagged):
//   - DeriveNonSystem — calls tenant.NewRowVisibility(RowScopeSelf, …); proves the
//     scan is symbol-specific (targets SystemRowVisibility, not "any RowVisibility
//     construction"), so the general constructor is never over-flagged.
package systemrowvisibilityfixture

import "github.com/ghbvf/gocell/framework/pkg/tenant"

// CallSystemRowVisibilityOutsideAllowlist is the RED case: a tenant-wide system
// obligation minted outside the sanctioned-caller allowlist. The funnel archtest
// must flag this call.
func CallSystemRowVisibilityOutsideAllowlist() tenant.RowVisibility {
	return tenant.SystemRowVisibility()
}

// DeriveNonSystem is a GREEN control: it constructs a subject-scoped obligation
// via the general constructor. It must NOT be flagged — the scan targets
// SystemRowVisibility, not every RowVisibility construction.
func DeriveNonSystem() tenant.RowVisibility {
	v, _ := tenant.NewRowVisibility(tenant.RowScopeSelf, "subject-x")
	return v
}
