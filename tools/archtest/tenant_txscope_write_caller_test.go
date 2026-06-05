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
// pre-auth / SystemTenantID sites to mint a principal tenant they do not have,
// eroding that key's "authenticated JWT tenant" meaning. The two funnels are
// orthogonal.
//
// # AI-robust rating (charter §"Funnel 双向锁评级") — fully closed (Hard/Hard)
//
// The two axes are separate; do not conflate the sealed key (an upstream
// property) with the caller-allowlist (the downstream one):
//
//   - Downstream (who may CALL WithScope): HARD. The archtest caller-allowlist
//     resolves the callee via go/types (ResolvePackageRef), so import aliases /
//     dot-imports resolve to the same symbol and any WithScope reference outside
//     the allowlist fails CI — the same downstream form as
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01.
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
// Reference-based (matches the SelectorExpr whether called or passed as a value),
// alias-proof via go/types. The anti-vacuity reverse check requires the
// allowlisted file to actually reference WithScope at least once, so a scanner
// regression or a removed call (which would make the funnel vacuously pass)
// fails CI.
//
// # Tool blind spot (charter §"强制盲区自检")
//
//   - Dot-import bare-identifier form (import . ".../pkg/tenant"; WithScope(…))
//     references the symbol as a bare *ast.Ident, not a SelectorExpr — not matched.
//     Dot-importing pkg/tenant is absent and conspicuous; documented, not enforced
//     (identical blind spot to CTXKEYS-PRINCIPAL-WRITE-CALLER-01).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"
)

// tenantPkgPath ("github.com/ghbvf/gocell/pkg/tenant") is declared in
// tenant_repo_param_funnel_test.go (same package); reused here.

// tenantWithScopeSetter is the funneled tenant.WithScope function name.
const tenantWithScopeSetter = "WithScope"

// tenantTxScopeAllowlist is the set of module-relative production files allowed
// to call tenant.WithScope. PR-3a: the single configcore read-scoping helper.
// PR-3b (#1617) will add the pre-auth/service derivation sites (setup / login /
// refresh) when accesscore tables go under RLS.
var tenantTxScopeAllowlist = map[string]struct{}{
	"cells/configcore/internal/scopedread/scopedread.go": {},
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
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !isTenantWithScopeRef(p.TypesInfo, sel) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := tenantTxScopeAllowlist[rel]; !allowed {
					pos := p.Fset.Position(sel.Pos())
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

// isTenantWithScopeRef resolves a SelectorExpr REFERENCE (call or function value)
// to tenant.WithScope in pkg/tenant (alias-proof via go/types).
func isTenantWithScopeRef(info *types.Info, sel *ast.SelectorExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	return ok && pkgPath == tenantPkgPath && name == tenantWithScopeSetter
}
