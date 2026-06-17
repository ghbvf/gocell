//go:build archtest

// audit_query_tenant_param_test.go — guards that the audit ledger read surface
// keeps its typed tenant.TenantID positional parameter on Query.
//
//   - INVARIANT: AUDIT-QUERY-TENANT-PARAM-01
//
// # What AUDIT-QUERY-TENANT-PARAM-01 guards (#1618 review F4)
//
// The audit Store is the one tenant-bearing repo NOT enrolled in
// TENANT-REPO-PARAM-FUNNEL-01 (its chain primitives Tail/Verify/GetBySeq derive
// tenant from ctx; only the post-auth read methods Query and GetByID (#1852) take
// the typed param — enrolling the whole Store would need five carve-outs, see ADR
// 202606071300 §threat-matrix). The audit TENANT axis instead rests on those
// methods' typed positional tenant.TenantID param (param[1], after ctx): on the mem
// backend it is the SOLE tenant isolation, and on PG it is the app-layer half of the
// FORCE-RLS dual-layer.
//
// The compiler makes that param Hard FOR CALLERS — given the current signature a
// caller cannot omit it. But the compiler does NOT lock the param's PRESENCE in
// the signature: a refactor could DROP it (updating callers mechanically) with no
// red build. ROWSCOPE-REPO-PARAM-FUNNEL-01 does not catch that either — its
// obligation slot is "param[2] when param[1] is tenant.TenantID, ELSE param[1]",
// so a Query that drops the tenant param simply lands the obligation at param[1]
// (its sanctioned GoodNoTenant form) and stays green. The ADR threat-matrix line
// that claimed ROWSCOPE "also catches omission" was over-stated (review F4); this
// archtest is the actual signature-presence lock.
//
// The scanner asserts that the tenant-bearing read methods (Query and GetByID,
// per auditReadTenantMethods) of every enrolled audit-read interface carry
// tenant.TenantID at param[1] (immediately after ctx), using go/types object
// identity (alias-proof).
//
// # Enrolled interfaces (runtime/audit/ledger)
//
//   - Store      — the audit ledger read/write surface (Query is its read method).
//   - QueryStore — the narrow read-only subset auditquery.Service binds to; the
//     real read-side composition boundary (a MultiStore read-aggregator is injected
//     as a QueryStore). Locking QueryStore.Query transitively covers the concrete
//     MultiStore.Query: MultiStore must satisfy QueryStore to be injectable, so a
//     tenant-param drop on MultiStore alone would fail interface satisfaction.
//
// # AI-robust rating
//
// AUDIT-QUERY-TENANT-PARAM-01 is a single-axis API-shape guard (not a funnel):
//   - upstream (caller) — compile-time Hard: given the signature, a caller cannot
//     omit the tenant arg, and tenant.TenantID is a sealed newtype so a bare string
//     cannot be passed.
//   - downstream (signature drift) — Medium: this archtest (type-aware go/types
//     param-identity scan; alias-proof) is the backstop against removing/​un-typing
//     the param from the signature itself, which the compiler permits.
//   - Hard-upgrade path: a sealed AuditReadScope handle making "audit Query without
//     a tenant scope" structurally unexpressible — the same ceiling as
//     TENANT-REPO-PARAM-FUNNEL-01 (gh #1478 / #1710 family). Tracked there; do not
//     patch at the Soft layer.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Only the methods in auditReadTenantMethods ("Query", "GetByID") are checked.
//     A future third tenant-scoped read method would need adding to that set —
//     guarded by the anti-vacuity check (zero methods scanned fails the test).
//   - Concrete (struct) types are out of scope: the scan enumerates explicit
//     interface methods. MultiStore (a struct) is covered transitively via the
//     QueryStore interface it must satisfy, not by direct scan.
//   - A Query that takes tenant.TenantID at param[1] but ignores it in the body is
//     not caught here (body-level concern; covered by the storetest conformance
//     Query_TenantIsolation / Per_Tenant_Chains cases that assert cross-tenant
//     reads return only the scoped tenant's rows).
//   - An ALIAS of tenant.TenantID still resolves to the same *types.Named via
//     go/types (isTenantIDType), so aliasing does not bypass — verified by the
//     FakeGoodQueryStore / FakeBadQueryStore reverse fixtures.
package archtest

import (
	"fmt"
	"go/types"
	"testing"
)

// auditQueryTenantParamFixPkg is a relative load path for go/packages — NOT a
// platform import path — so it is intentionally not derived from PlatformModulePath.
const auditQueryTenantParamFixPkg = "./tools/archtest/internal/auditquerytenantparamfixture"

// auditQueryReadIfaces are the audit-read interfaces whose tenant-bearing read
// methods must carry a typed tenant.TenantID at param[1]. Looked up via
// Scope.Lookup in the enrolled ledger package; absent names resolve to nil and are
// skipped.
var auditQueryReadIfaces = []string{"Store", "QueryStore"}

// auditReadTenantMethods are the audit-read interface methods whose TENANT axis
// rests on a typed tenant.TenantID at param[1] (after ctx): Query (the list path)
// and GetByID (the single-entry path, #1852). Both are post-auth read surfaces that
// take the explicit typed tenant — unlike the ctx-scoped chain primitives
// Tail/Verify/GetBySeq. A method outside this set (e.g. GetBySeq) is intentionally
// skipped (it derives tenant from ctx, not a positional param).
var auditReadTenantMethods = map[string]bool{"Query": true, "GetByID": true}

// tenantIDAtParam1 reports whether sig is (ctx context.Context, t tenant.TenantID, …)
// — the audit-Query tenant-axis contract. Uses go/types object identity
// (isContextType / isTenantIDType, declared in tenant_repo_param_funnel_test.go),
// so import aliases and type aliases resolve correctly.
func tenantIDAtParam1(sig *types.Signature) bool {
	params := sig.Params()
	if params.Len() < 2 {
		return false
	}
	return isContextType(params.At(0).Type()) && isTenantIDType(params.At(1).Type())
}

// scanAuditQueryTenantParam reports every Query method on the named interfaces in
// p.Pkg whose param[1] is not tenant.TenantID. seen counts Query methods observed
// (anti-vacuity signal).
func scanAuditQueryTenantParam(p *Pass, ifaceNames []string) (diags []Diagnostic, seen int) {
	scope := p.Pkg.Scope()
	for _, ifaceName := range ifaceNames {
		obj := scope.Lookup(ifaceName)
		if obj == nil {
			continue
		}
		iface, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}
		for i := 0; i < iface.NumExplicitMethods(); i++ {
			m := iface.ExplicitMethod(i)
			if !auditReadTenantMethods[m.Name()] {
				continue
			}
			seen++
			sig, _ := m.Type().(*types.Signature)
			if sig != nil && tenantIDAtParam1(sig) {
				continue
			}
			pos := p.Fset.Position(m.Pos())
			diags = append(diags, Diagnostic{
				Line: pos.Line,
				Message: fmt.Sprintf(
					"AUDIT-QUERY-TENANT-PARAM-01: %s.%s must carry tenant.TenantID at param[1] (after ctx). "+
						"The audit TENANT axis rests on this typed positional scope (sole isolation on mem; the "+
						"app-layer half of FORCE-RLS on PG). Dropping or un-typing it type-checks and PASSES "+
						"ROWSCOPE-REPO-PARAM-FUNNEL-01 (no-tenant form), so this lock is the only guard against the "+
						"silent tenant-axis removal.",
					ifaceName, m.Name(),
				),
			})
		}
	}
	return diags, seen
}

// TestAuditQueryTenantParam01 asserts Store.Query and QueryStore.Query in
// runtime/audit/ledger carry tenant.TenantID at param[1].
func TestAuditQueryTenantParam01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var seen int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil || p.Pkg.Path() != ledgerStorePkg {
			return nil
		}
		d, s := scanAuditQueryTenantParam(p, auditQueryReadIfaces)
		seen += s
		return d
	})

	// Anti-vacuity: the scan must have resolved a real Query method (else a path /
	// typing regression would make this test vacuously pass).
	if seen == 0 {
		diags = append(diags, Diagnostic{Message: "AUDIT-QUERY-TENANT-PARAM-01: scanned zero Query methods — " +
			"scanner regressed or the enrolled ledger package path changed (expected " + ledgerStorePkg + ")"})
	}

	Report(t, "AUDIT-QUERY-TENANT-PARAM-01", diags)
}

// TestAuditQueryTenantParam01_ScannerCatchesViolation is the reverse self-check:
// it runs the SAME scanner against the RED/GREEN fixture and asserts both
// tenant-bearing methods of FakeBadQueryStore (Query AND GetByID without the tenant
// param) are flagged while FakeGoodQueryStore's are not. Expects exactly 2
// diagnostics (one per bad read method), proving the GetByID enrollment (#1852) is
// machine-checked, not just Query.
func TestAuditQueryTenantParam01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{auditQueryTenantParamFixPkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		d, _ := scanAuditQueryTenantParam(p, []string{"FakeGoodQueryStore", "FakeBadQueryStore"})
		return d
	})

	if len(diags) != 2 {
		t.Fatalf("expected exactly 2 diagnostics (FakeBadQueryStore.Query + .GetByID missing tenant param), got %d: %v", len(diags), diags)
	}
}
