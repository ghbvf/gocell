//go:build archtest

// ctxkeys_tenant_read_caller_test.go — closes the bypass face of the tenant-scope
// derivation funnel (#1882 / #1883): pins every production reader of the raw tenant
// ctx key to a bounded allowlist, forcing business code through the fail-closed typed
// accessor tenant.FromContext.
//
//   - INVARIANT: CTXKEYS-TENANT-READ-CALLER-01
//
// # What this guards
//
// A saga-journal projection replays under the system principal: InstallSystemPrincipal
// sets the tenant ctx key to "" (kernel/projection/system_principal.go; ADR #1609 §5).
// The danger #1882 names is an Apply / business handler that reads the raw tenant ctx
// key, observes the empty string, and treats it as cross-tenant authority — e.g.
// `if tid, _ := ctxkeys.TenantIDFrom(ctx); tid == "" { /* system: write all tenants */ }`
// — an implicit privilege escalation the global (cross-tenant) saga journal makes
// reachable (#1883).
//
// The sanctioned typed accessor tenant.FromContext already fail-closes that read HARD:
// it routes the raw value through tenant.ParseTenantID, which REJECTS the empty string
// (and the reserved nil UUID), returning KindPermissionDenied (403). A handler that
// derives its tenant scope via the typed accessor therefore CANNOT obtain a footgun ""
// — the misjudgment is unexpressible on the sanctioned path. The only way to express
// it is to BYPASS the typed accessor and read the raw key via ctxkeys.TenantIDFrom.
//
// This archtest pins every production REFERENCE of ctxkeys.TenantIDFrom (the only raw
// reader of the tenant ctx key) to a bounded allowlist of infra readers. Any new
// reader — in particular a business / Apply / handler — fails CI until added here with
// a rationale, forcing it back through the fail-closed typed accessor. It is the
// bypass-face complement to the typed accessor's Hard empty-rejection; together they
// make "empty ctx tenant ⟹ cross-tenant authority" unexpressible in business code.
//
// # Legitimate readers (today)
//
//   - pkg/tenant/context.go — tenant.FromContext, THE sanctioned typed accessor this
//     funnel forces everyone else through (it parses + fail-closes on empty/nil).
//   - kernel/outbox/principal.go — PrincipalMetadata capture: reads the ambient tenant
//     to stamp the outbox envelope's principal (a tenant-less system emit stamps "").
//     Referenced twice: a direct read and a function-value pass to withContextMetadata.
//   - adapters/postgres/tx_manager.go — RLS GUC injection: reads the authenticated
//     principal's tenant to SET LOCAL app.tenant_id for the transaction. Already guards
//     the empty case (`ok && raw != ""` → no GUC write).
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD by archtest caller-allowlist. The callee is resolved via
//     go/types (ResolvePackageRef), so import aliases and dot-imported bare idents
//     resolve to the same symbol, and function-value references (the principal.go pass
//     to withContextMetadata) are caught too. Any reader outside the allowlist fails CI.
//   - Upstream: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Hard upstream would
//     require TenantIDFrom to be unreachable outside the allowlist; it cannot be sealed
//     — pkg/ctxkeys must export it for pkg/tenant, kernel/outbox and adapters/postgres
//     (different packages, different modules) to call, and Go visibility cannot express
//     "only these N files may call this exported func". Same permanent ceiling as
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (#1282) / SPAN-SETATTR-HOLDER-SEAL (#851) /
//     HEALTHZ-HOLDER-SEAL (#893). The combined guard is: typed-accessor empty-rejection
//     (Hard, existing) + this bypass-face caller-funnel (Medium). The fail-closed typed
//     accessor is the enforcement; this archtest pins the only path around it.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection is REFERENCE-based and scans every *ast.Ident (go/types resolves it to
//     TenantIDFrom whether it is the .Sel of a qualified ctxkeys.TenantIDFrom, a
//     dot-imported bare TenantIDFrom, OR a function value). The principal.go
//     function-value pass to withContextMetadata is the load-bearing non-call case.
//   - //go:build-gated production files under a non-default tag are missed by the
//     default-tags scan.
//   - The anti-vacuity guard (every allowlisted file must reference TenantIDFrom ≥1×)
//     is the reverse self-check: it proves the scanner resolves the real references and
//     forbids a stale entry becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"
)

// tenantReadAllowlist is the set of module-relative production files allowed to
// reference ctxkeys.TenantIDFrom (the raw tenant ctx-key reader). Everyone else must
// derive tenant scope via the fail-closed typed accessor tenant.FromContext. See the
// file godoc for each entry's rationale.
var tenantReadAllowlist = map[string]struct{}{
	"pkg/tenant/context.go":           {}, // tenant.FromContext — THE typed accessor (parses + fail-closes on empty/nil)
	"kernel/outbox/principal.go":      {}, // outbox envelope principal capture (direct read + function-value pass)
	"adapters/postgres/tx_manager.go": {}, // RLS GUC injection; already guards empty (ok && raw != "")
}

// TestCtxkeysTenantReadCaller01 asserts every production reference of
// ctxkeys.TenantIDFrom sits in tenantReadAllowlist, and that no allowlist entry is
// stale (anti-vacuity reverse check).
func TestCtxkeysTenantReadCaller01(t *testing.T) {
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
			// Scan every *ast.Ident (not just SelectorExpr): ResolvePackageRef
			// resolves both the `.Sel` of a qualified `ctxkeys.TenantIDFrom` AND a
			// bare `TenantIDFrom` from a dot-import. Each callsite has exactly one
			// matching ident, so there is no double-count.
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isTenantIDFromRef(p.TypesInfo, id) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := tenantReadAllowlist[rel]; !allowed {
					pos := p.Fset.Position(id.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"CTXKEYS-TENANT-READ-CALLER-01: ctxkeys.TenantIDFrom is referenced from %s, which is "+
								"not a sanctioned raw tenant reader. Reading the raw tenant ctx key risks the #1882 "+
								"footgun: during saga-journal replay the system principal carries an empty tenant, and "+
								"treating empty as cross-tenant authority is an implicit privilege escalation (#1883). "+
								"Derive tenant scope via the fail-closed typed accessor tenant.FromContext (it rejects "+
								"empty/nil → 403) instead. If this IS a new sanctioned infra reader, add it to "+
								"tenantReadAllowlist with a rationale.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check.
	allowed := make([]string, 0, len(tenantReadAllowlist))
	for f := range tenantReadAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"CTXKEYS-TENANT-READ-CALLER-01: allowlist entry %q is STALE — no live ctxkeys.TenantIDFrom "+
						"reference observed. Either the scanner regressed or the reference was removed; drop the "+
						"dead allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "CTXKEYS-TENANT-READ-CALLER-01", diags)
}

// isTenantIDFromRef reports whether expr is a REFERENCE (call, function value, or
// dot-imported bare ident) to pkg/ctxkeys.TenantIDFrom, alias-proof via go/types.
// ResolvePackageRef accepts both *ast.SelectorExpr and *ast.Ident.
func isTenantIDFromRef(info *types.Info, expr ast.Expr) bool {
	pkgPath, name, ok := ResolvePackageRef(info, expr)
	return ok && pkgPath == ctxkeysPkgPath && name == "TenantIDFrom"
}
