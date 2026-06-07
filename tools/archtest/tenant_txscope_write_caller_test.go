// tenant_txscope_write_caller_test.go — closes the DOWNSTREAM side of the PR-3
// transaction tenant-scope write boundary.
//
//   - INVARIANT: TENANT-TXSCOPE-WRITE-CALLER-01
//
// # What this guards
//
// tenant.WithScope(ctx, t) sets the value that adapters/postgres.RunInTx
// interpolates into the per-transaction RLS GUC (set_config('app.tenant_id', …,
// true)). It is therefore the tenant-isolation write boundary for any path that
// does not already carry an authenticated-principal ctxkeys.TenantID. If business
// code could call WithScope freely, it could scope a read/write transaction to
// an arbitrary tenant, defeating RLS through the blessed path.
//
// This archtest pins every production reference to tenant.WithScope to the sole
// sanctioned caller:
//
//   - cells/configcore/internal/scopedread/scopedread.go — Do(), the one helper
//     that wraps configcore reads in a tenant-scoped transaction. It scopes to
//     the SAME TenantID the repo query filters on, so the RLS GUC and the WHERE
//     predicate are consistent by construction.
//
// Post-auth handlers do NOT call WithScope: their tenant flows from the
// authenticated principal via the RunInTx ctxkeys.TenantID fallback, which keeps
// this allowlist minimal (the tighter the funnel, the smaller the attack surface).
//
// # Why a dedicated key, not ctxkeys.TenantID
//
// WithScope writes a dedicated, unexported scope key (pkg/tenant/scope.go), kept
// separate from the principal carrier ctxkeys.TenantID (locked by
// CTXKEYS-PRINCIPAL-WRITE-CALLER-01). Reusing the principal key would have forced
// pre-auth paths (service-token callers without a JWT tenant in ctx) to mint a
// principal tenant they do not have, eroding that key's "authenticated JWT tenant"
// meaning. The two funnels are orthogonal.
//
// # AI-robust rating (charter §"Funnel 双向锁评级") — fully closed (Hard/Hard)
//
// The two axes are separate; do not conflate the sealed key (an upstream
// property) with the caller-allowlist (the downstream one):
//
//   - Downstream (who may CALL WithScope): HARD. The detector resolves every
//     identifier USE to the tenant.WithScope *types.Func via go/types (info.Uses),
//     so the package-qualified form (tenant.WithScope — the Sel ident), an
//     import-aliased form, AND the dot-imported bare-identifier form all resolve
//     to the same object; any WithScope reference outside the allowlist fails CI,
//     no import shape excepted. (#1622 F3 closed the prior SelectorExpr-only gap,
//     where a dot-import bare ident was a documented-but-unenforced blind spot
//     that contradicted this Hard claim.)
//   - Upstream (can the scope be SET without WithScope): HARD. The scope is read
//     only via tenant.ScopeFromContext, whose key (scopeKey) is UNEXPORTED — no
//     package outside pkg/tenant can construct it, so the tx scope cannot be set
//     except through WithScope (sealed construction; "包外不可表达跳过"). There is
//     NO alternative scope-write path, so — unlike the principal-write funnel
//     (#1282, Medium upstream because identities have other forge paths) — this
//     funnel is closed on both axes; no Hard-upgrade issue is needed.
//
// # Detection + anti-vacuity
//
// Use-based (info.Uses), so it matches WithScope whether called or passed as a
// value, and is invariant to import form (qualified / alias / dot-import). The
// dot-import path additionally carries a RED fixture
// (internal/tenantscopefixture, exercised by TestTenantTxScopeWriteCaller01_
// FixtureCatchesDotImport) proving the detector fires on a bare-identifier scope
// write outside the allowlist. The anti-vacuity reverse check requires the
// allowlisted file to actually reference WithScope at least once, so a scanner
// regression or a removed call (which would make the funnel vacuously pass)
// fails CI.
//
// # Tool blind spot (charter §"强制盲区自检")
//
//   - A scope write assembled by reflection, or otherwise without a compile-time
//     reference to the WithScope symbol, is invisible — the same known limit as
//     every identifier-resolution funnel in this suite. Both the package-qualified
//     and dot-import textual forms ARE covered (see the RED fixture); dot-importing
//     pkg/tenant is additionally banned by revive `dot-imports` (.golangci.yml) as
//     defense-in-depth.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"
)

// tenantPkgPath ("github.com/ghbvf/gocell/pkg/tenant") is declared in
// tenant_repo_param_funnel_test.go (same package); reused here.

// tenantWithScopeSetter is the funneled tenant.WithScope function name.
const tenantWithScopeSetter = "WithScope"

// tenantTxScopeAllowlist is the set of module-relative production files allowed
// to call tenant.WithScope. PR-3a: the single configcore read-scoping helper.
// PR-3b (#1617): accesscore routes ALL its pre-auth/service tenant-scoping (setup,
// login 2-tx, refresh reuse-cascade, validate, rbaccheck, authorizationdecide,
// rbacassign, IssueForUser) through the single scopedtx funnel — so the allowlist
// gains exactly ONE accesscore entry (scopedtx.Do is the only WithScope callsite;
// the mid-tx ApplyScope path does not call WithScope, it writes the GUC directly).
// #1618 (audit per-tenant chain): the cross-backend audit ledger conformance suite
// is the SINGLE source that exercises the ctx-scoped chain reads (GetBySeq / Verify
// / Tail derive their tenant from tenant.ScopeFromContext), so its scoped-read
// helpers must call WithScope to set up each per-tenant scope. It is test-support
// (a non-_test.go helper imported by both the mem and PG store test packages, so it
// cannot itself be _test.go and lands in Production() scan scope); it is the sole
// scope writer on the audit conformance side, mirroring the scopedread/scopedtx
// production funnels.
var tenantTxScopeAllowlist = map[string]struct{}{
	"cells/configcore/internal/scopedread/scopedread.go": {},
	"cells/accesscore/internal/scopedtx/scopedtx.go":     {},
	"runtime/audit/ledger/storetest/suite.go":            {},
	// auditcoretest.BuildAuditcoreChain returns a tenant-scoped ctx so that
	// Tail/Verify read-back targets the per-tenant chain its entries land in
	// (#1618). Same test-support precedent as storetest/suite.go (non-_test.go
	// helper imported by other test packages, so it lands in Production() scan).
	"cells/auditcore/auditcoretest/builders.go": {},
}

// TestTenantTxScopeWriteCaller01 asserts every production reference to
// tenant.WithScope sits in tenantTxScopeAllowlist, and that the allowlist entry
// is not stale (anti-vacuity reverse check).
func TestTenantTxScopeWriteCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isTenantWithScopeRef(p.TypesInfo, id) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := tenantTxScopeAllowlist[rel]; !allowed {
					pos := p.Fset.Position(id.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"TENANT-TXSCOPE-WRITE-CALLER-01: tenant.WithScope is referenced from %s, which is not a "+
								"sanctioned tx-scope writer. WithScope sets the value RunInTx injects into the RLS "+
								"app.tenant_id GUC, so it is the tenant-isolation write boundary. Business code must let "+
								"the tenant flow from the authenticated principal (ctxkeys.TenantID fallback); only the "+
								"scopedread helper (and PR-3b pre-auth derivation sites) may scope a transaction. If this "+
								"IS a new sanctioned writer, add it to tenantTxScopeAllowlist with rationale.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check.
	for f := range tenantTxScopeAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"TENANT-TXSCOPE-WRITE-CALLER-01: allowlist entry %q is STALE — no live tenant.WithScope "+
						"reference observed. Either the scanner regressed or the call was removed; drop the dead "+
						"allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "TENANT-TXSCOPE-WRITE-CALLER-01", diags)
}

// TestTenantTxScopeWriteCaller01_FixtureCatchesDotImport is the reverse self-check
// for the dot-import path (#1622 F3): the RED fixture dot-imports pkg/tenant and
// writes a scope via a BARE `WithScope` ident. The Use-based detector must resolve
// that bare ident (info.Uses) and flag the fixture file — it is outside the
// allowlist. A 0 result means the detector regressed to a SelectorExpr-only form,
// reopening dot-import as a silent bypass.
func TestTenantTxScopeWriteCaller01_FixtureCatchesDotImport(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkg := modPath + "/tools/archtest/internal/tenantscopefixture"
	pattern := "./tools/archtest/internal/tenantscopefixture/..."
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isTenantWithScopeRef(p.TypesInfo, id) {
					return
				}
				pos := p.Fset.Position(id.Pos())
				d = append(d, Diagnostic{Rel: rel, Line: pos.Line, Message: "dot-import WithScope reference"})
			})
		}
		return d
	})
	for _, dd := range diags {
		t.Log(dd.Message)
	}
	require.Len(t, diags, 1,
		"the Use-based detector must resolve the dot-imported bare WithScope ident; "+
			"a 0 result means it regressed to SelectorExpr-only and dot-import is a bypass")
}

// isTenantWithScopeRef resolves an identifier USE to tenant.WithScope in pkg/tenant
// via go/types (info.Uses). It matches every import form: the package-qualified
// `tenant.WithScope` (the Sel ident resolves through Uses), an import-aliased form,
// AND the dot-imported bare `WithScope` ident — all resolve to the same *types.Func,
// so no import shape can slip a scope write past the allowlist (#1622 F3). A bare
// ident that is NOT this func (package names, other symbols) resolves to a
// different object (or none) and is skipped.
func isTenantWithScopeRef(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	return fn.Name() == tenantWithScopeSetter &&
		fn.Pkg() != nil && fn.Pkg().Path() == tenantPkgPath
}
