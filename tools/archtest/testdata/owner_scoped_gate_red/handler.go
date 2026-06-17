// Package owner_scoped_gate_red is a synthetic RED fixture for the
// OWNER-SCOPED-GATE-EXACT-SET-01 archtest. It defines owner-scoped route gates with
// the two real drift modes the rule must observe:
//
//   - a path-param drift ("wrongParam" instead of the expected param),
//   - a regression from auth.RequirePermissionForResource to plain
//     auth.RequirePermission (which drops the owner gate entirely), and
//   - the same well-formed/regressed pair for the RequirePermissionForSelf gate
//     shape (#1863): a well-formed self gate is collected as a "self" triple, while
//     a self gate degraded to plain auth.RequirePermission is not collected.
//
// The archtest's reverse self-check scans this module to prove its extraction is not
// vacuous. DO NOT use this package in production code.
package owner_scoped_gate_red

import (
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// gates exercises the owner-scoped gate shapes the archtest collects.
func gates() {
	// Well-formed owner gate — collected as "id|PermUserRead".
	_ = auth.RequirePermissionForResource("id", authz.PermUserRead())
	// Param drift — collected as "wrongParam|PermUserWrite" (observable as a changed triple).
	_ = auth.RequirePermissionForResource("wrongParam", authz.PermUserWrite())
	// REGRESSION: a plain permission gate for what should be an owner endpoint. It
	// forwards r.URL.Path, not the canonical resource id, so the PDP ownership rule
	// never matches. It is NOT a RequirePermissionForResource call, so the archtest
	// does not collect it — in production this shows up as a MISSING owner-gate triple.
	_ = auth.RequirePermission(authz.PermRoleRead())

	// Well-formed self-resource owner gate (#1863) — collected as "self|PermSystemRead".
	// Proves the RequirePermissionForSelf branch of collectOwnerScopedGates is exercised.
	_ = auth.RequirePermissionForSelf(authz.PermSystemRead())
	// REGRESSION of a self-gate to plain RequirePermission: forwards r.URL.Path, not the
	// caller's subject, so the self ownership rule never matches. Not a RequirePermissionForSelf
	// call → not collected as a "self" triple → MISSING in production.
	_ = auth.RequirePermission(authz.PermConfigRead())
}
