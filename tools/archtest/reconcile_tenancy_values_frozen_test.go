//go:build archtest

// INVARIANT: RECONCILE-TENANCY-DECLARED-01
//
// This file owns ONE invariant: the set of exported, nullary minters that return
// the sealed kernel/reconcile.Tenancy type — the closed set of tenancy stances a
// reconciler may declare at reconcile.New — is frozen to exactly:
//
//	{"SingleTenant", "TenantScoped"}
//
// Background (#1954): a reconcile Loop runs under a positively-installed,
// TENANTLESS system producer identity (installSystemProducerIdentity), so every
// emitted command's Claimer dedup key lands under the "_notenant" sentinel BY
// CONSTRUCTION. That is correct for a single-tenant archetype (entity ids are
// globally unique), but a MULTI-TENANT reconciler must encode the tenant in its
// OWN command-id derivation — otherwise two tenants sharing an entity id collide
// on one "_notenant" key and one tenant's work silently suppresses the other's.
// Before #1954 that obligation was a pure godoc MUST (Soft, forbidden by
// ai-robust.md). reconcile.New now takes a REQUIRED, sealed Tenancy second
// parameter, so "didn't think about the tenant dimension" is unrepresentable at
// construction. This invariant is the external witness that the stance set stays
// a binary {single, scoped} and is not silently widened.
//
// # AI-robust rating (four axes — the mechanism is stronger than the Medium this
//
//	invariant minimally provides; see kernel/reconcile/tenancy.go godoc)
//
//	- Axis 1 — forging a non-zero Tenancy: type-system HARD. The mode field is
//	  unexported and the minters are accessor funcs over package-private
//	  singletons (un-reassignable), so reconcile.Tenancy{mode: …} is a compile
//	  error outside kernel/reconcile (sealed construction 范本).
//	- Axis 2 — omitting the stance entirely: compile HARD. Tenancy is a positional
//	  parameter of reconcile.New, so reconcile.New(r) does not compile — you must
//	  pass a Tenancy expression. Stronger than a forgettable WithTenancy() option.
//	- Axis 3 — passing the zero Tenancy{}: runtime fail-fast (Build rejects it,
//	  TestBuilder_RequiresTenancy). The one non-compile-Hard escape hatch; a
//	  permanent Go ceiling (a zero-value struct is always constructible), the same
//	  accepted residual as authz.Permission{} / reconcile.LeaseTTL{}.
//	- Axis 4 — the minter VALUE SET (THIS test): MEDIUM. go/types enumeration of
//	  exported nullary Tenancy-returning funcs vs an independent hardcoded
//	  want-set. Not Hard because an in-package author can add a 3rd minter and the
//	  compiler does not object; this archtest is the frozen-witness that keeps the
//	  stance vocabulary a deliberate binary.
//
// No arity-freeze archtest is added for axis 2: requiring the parameter is already
// compile-enforced (dropping it breaks every call site), and pinning New's arity
// would exceed the established convention (the equally-required Trigger is guarded
// by a Build check + unit test, not an archtest).
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Body correctness is OUT OF SCOPE: the guard forces a TenantScoped()
//     DECLARATION but cannot verify the reconciler's body actually adds the tenant
//     to its command-id / store derivation. That is consumer correctness, outside
//     any framework's reach (analogous to RECONCILE-FENCED-WRITE-FUNNEL-01's
//     ApplyFenced-ignores-epoch admission). Accepted; no Hard path exists.
//   - Mis-declaration is OUT OF SCOPE: a consumer can declare SingleTenant() inside
//     an actual multi-tenant cell. There is NO cell-level tenancy signal (multi-
//     tenancy is a runtime ctxkeys.TenantID property from JWT, not a static cell
//     attribute), so the framework cannot statically cross-check the declaration.
//     The guard converts an unconscious omission into a conscious (possibly wrong)
//     assertion — strictly better, not a correctness proof.
//   - This test runs on the kernel/reconcile package Pass only (filtered by
//     p.Pkg.Path()). That out-of-package scope is NOT a gap but the funnel's
//     DOWNSTREAM lock: a nullary func in another package CANNOT return a non-zero
//     Tenancy because the mode field is unexported (Axis 1, type-system Hard), so no
//     out-of-package minter can exist to scan. Funnel strength is two-sided —
//     upstream/downstream = Axis 1 sealed construction (Hard), this archtest = the
//     in-package value-set freeze (Axis 4, Medium).
//
// # Reverse self-check (non-vacuous proof)
//
// TestReconcileTenancyDeclared01_NegativeControl demonstrates that a synthetic 3rd
// minter or a renamed minter is detected.
package archtest

import (
	"go/types"
	"testing"
)

// wantTenancyMinters is the frozen membership of the reconcile tenancy-stance set.
// Updating this list requires reviewer attention and a simultaneous update to:
// (1) kernel/reconcile/tenancy.go minters, (2) the Build fail-fast in builder.go,
// (3) the identity.go / doc.go / ADR-1821 / reconcile.md prose, and (4) every
// reconcile.New call site (each must pick a stance).
var wantTenancyMinters = []string{
	"SingleTenant",
	"TenantScoped",
}

// tenancyType returns the kernel/reconcile package's sealed Tenancy named type, or
// (nil, false) if it is absent (renamed/removed).
func tenancyType(p *Pass) (types.Type, bool) {
	if p.Pkg == nil {
		return nil, false
	}
	obj := p.Pkg.Scope().Lookup("Tenancy")
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return nil, false
	}
	return tn.Type(), true
}

// collectTenancyMinters enumerates the names of every exported, package-scope,
// nullary func in p (which must be the kernel/reconcile package) whose sole result
// is the sealed Tenancy type. Enumerating BY return-type identity (not by name
// prefix) makes the freeze rename-proof: a minter renamed off "*Tenant*" but still
// returning Tenancy is still counted, and a 3rd minter is still caught. Methods
// (IsUnset) are not package-scope objects, so they are not enumerated; New returns
// *Builder, not Tenancy, so it is excluded.
func collectTenancyMinters(p *Pass) []string {
	if p.Pkg == nil {
		return nil
	}
	tType, ok := tenancyType(p)
	if !ok {
		return nil
	}
	scope := p.Pkg.Scope()
	var names []string
	for _, name := range scope.Names() {
		fn, ok := scope.Lookup(name).(*types.Func)
		if !ok || !fn.Exported() {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() != nil {
			continue
		}
		if sig.Params().Len() != 0 || sig.Results().Len() != 1 {
			continue
		}
		if !types.Identical(sig.Results().At(0).Type(), tType) {
			continue
		}
		names = append(names, fn.Name())
	}
	return names
}

// TestReconcileTenancyDeclared01 freezes the exported Tenancy minter set in
// kernel/reconcile against the independent hardcoded wantTenancyMinters (anti-
// tautology). Both directions: nothing missing, nothing extra.
func TestReconcileTenancyDeclared01(t *testing.T) {
	t.Parallel()

	const reconcilePkg = PlatformModulePath + "/kernel/reconcile"
	var got []string

	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != reconcilePkg {
			return nil
		}
		got = collectTenancyMinters(p)
		return nil
	})

	if len(got) == 0 {
		t.Fatalf("RECONCILE-TENANCY-DECLARED-01: found 0 exported nullary Tenancy minters in %s — "+
			"did the package path change, or were SingleTenant/TenantScoped renamed/removed?", reconcilePkg)
	}

	// resultValuesDiff (reconcile_result_label_values_frozen_test.go, same package)
	// is a generic order-insensitive set diff — reused here to avoid duplication.
	if diff := resultValuesDiff(got, wantTenancyMinters); diff != "" {
		t.Fatalf("RECONCILE-TENANCY-DECLARED-01: the exported Tenancy minter set in kernel/reconcile "+
			"drifted from the frozen want-set.\n%s\n"+
			"The reconciler tenancy-stance vocabulary is a deliberate binary {SingleTenant, TenantScoped}. "+
			"If this change is intentional, update ALL sync points in the same PR: "+
			"(1) wantTenancyMinters here, (2) kernel/reconcile/tenancy.go, (3) the Build fail-fast in "+
			"builder.go, (4) identity.go / doc.go / ADR-1821 / .claude/rules/gocell/reconcile.md.", diff)
	}

	// Reverse self-check: assert exactly 2 minters exist (the same count as the
	// want-set). A 3rd minter would produce an extra entry AND increment this count.
	if len(got) != len(wantTenancyMinters) {
		t.Errorf("RECONCILE-TENANCY-DECLARED-01: found %d exported Tenancy minters, want exactly %d "+
			"— a 3rd stance was added without updating the golden; see wantTenancyMinters",
			len(got), len(wantTenancyMinters))
	}
}

// TestReconcileTenancyDeclared01_NegativeControl proves the comparison is
// non-vacuous: a synthetically drifted set (a 3rd minter added, one renamed) MUST
// produce a non-empty diff (blind-spot self-check A per ai-robust.md).
func TestReconcileTenancyDeclared01_NegativeControl(t *testing.T) {
	t.Parallel()

	// Extra minter "MultiRegion" — a forbidden 3rd stance.
	withExtra := []string{"SingleTenant", "TenantScoped", "MultiRegion"}
	if diff := resultValuesDiff(withExtra, wantTenancyMinters); diff == "" {
		t.Fatal("RECONCILE-TENANCY-DECLARED-01 negative control: a set with extra 'MultiRegion' " +
			"produced an empty diff — the comparison is vacuous and would not catch a real drift")
	}

	// Missing minter — "TenantScoped" renamed to "MultiTenant".
	withRenamed := []string{"SingleTenant", "MultiTenant"}
	if diff := resultValuesDiff(withRenamed, wantTenancyMinters); diff == "" {
		t.Fatal("RECONCILE-TENANCY-DECLARED-01 negative control: a set with 'TenantScoped' " +
			"renamed to 'MultiTenant' produced an empty diff — the comparison is vacuous")
	}

	// Correct set must produce empty diff (over-fire guard).
	if diff := resultValuesDiff(wantTenancyMinters, wantTenancyMinters); diff != "" {
		t.Fatalf("RECONCILE-TENANCY-DECLARED-01 negative control: the frozen want-set compared against "+
			"itself produced a non-empty diff — the comparison has a bug: %s", diff)
	}
}
