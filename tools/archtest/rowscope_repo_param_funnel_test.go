// rowscope_repo_param_funnel_test.go — guards that every row-scoped list/get
// repo interface method carries a tenant.RowVisibility obligation positional
// parameter. Enrolled repos: auditcore (runtime/audit/ledger.Store).
//
//   - INVARIANT: ROWSCOPE-REPO-PARAM-FUNNEL-01
//
// # What ROWSCOPE-REPO-PARAM-FUNNEL-01 guards (#1337 PR-4 / #1342)
//
// Row-level visibility in GoCell is enforced as a mandatory typed positional
// parameter — tenant.RowVisibility, the self-contained {RowScope, subject}
// obligation (XACML <Obligation> / Oso Filter value-object shape) — on the
// row-scoped repo interfaces. The DOMINANT guarantee is the compiler: once
// ledger.Store.Query(ctx, vis tenant.RowVisibility, …) exists, a caller that
// "forgets the obligation" cannot compile, and because RowVisibility is sealed
// (unexported fields, sole constructor NewRowVisibility) a caller cannot FORGE a
// RowScopeAll (cross-tenant) obligation by struct literal either — no fixture
// needed for those vectors.
//
// This archtest guards the COMPLEMENTARY drift vector the compiler cannot catch:
// a NEW (or edited) row-scoped read method whose obligation slot is declared as a
// plain type instead of tenant.RowVisibility, OR where tenant.RowVisibility
// appears at the wrong position. Such a method type-checks fine but reopens the
// "no row-visibility predicate / wrong-typed obligation" hole.
//
// The scanner asserts that every explicit method of the enrolled repo interfaces,
// except an explicit carve-out allowlist, carries tenant.RowVisibility at the
// OBLIGATION SLOT:
//
//  1. Param[0] is context.Context.
//  2. The obligation slot is param[2] when param[1] is tenant.TenantID (the
//     accesscore-style positional-tenant shape), else param[1] (the ledger.Store
//     shape, whose tenant boundary lives in AuditFilters, not a positional param).
//     The slot must be exactly tenant.RowVisibility (go/types object identity —
//     alias-proof).
//
// # Enrolled interfaces
//
// runtime/audit/ledger (ledgerStorePkg):
//   - Store — the audit ledger read surface. Carve-out: Protocol / Append / Tail /
//     Verify / RepoReady (see allowlist).
//
// PR-4 scope is auditcore ONLY. accesscore (UserRepository / RoleRepository) and
// configcore (ConfigRepository / FlagRepository) reads are auth-internal /
// tenant-or-global SYSTEM reads with no roadmapped self/device consumer, so they
// are deliberately NOT enrolled here (they would carry a permanently-dead
// obligation param). They enroll when a subject-self read endpoint appears —
// tracked at gh #1709.
//
// # Carve-out allowlist (non-row reads on ledger.Store)
//
//   - Store.Protocol — returns immutable protocol decisions; not a row read.
//   - Store.Append — write path (persist an entry); not a row read.
//   - Store.Tail — chain-tail snapshot (SeqNo/PrevHash/count); namespace-global,
//     not a subject-owned row read.
//   - Store.Verify — re-computes the HMAC chain across [from,to]; integrity scan,
//     not a subject-scoped read.
//   - Store.RepoReady — healthz relation probe; not a data read.
//
// Store.Query and Store.GetBySeq have NO carve-out: both return subject-owned
// rows (owner column actor_id) and MUST carry the obligation.
//
// # AI-robust ratings
//
// ROWSCOPE-REPO-PARAM-FUNNEL-01: the compiler-Hard "漏传 obligation = 编译失败"
// (typed positional param) + sealed-construction "伪造 all = 编译失败" are the
// primary gates (upstream Hard). This archtest is the MEDIUM backstop against the
// plain-typed-slot AND wrong-position drifts the compiler permits (type-aware
// AST/types scan; alias-proof). It is the same rating shape as
// TENANT-REPO-PARAM-FUNNEL-01.
//   - Hard-upgrade path (downstream): a sealed RowScopedRepo handle making "query
//     without an obligation" structurally unexpressible — the same ceiling as
//     TENANT-REPO-PARAM-FUNNEL-01 (gh #1478 family). Tracked at gh #1710; do not
//     patch at the Soft layer.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Non-interface (struct-method) repos are out of scope: the scan enumerates
//     explicit interface methods. A future concrete-type row-scoped repo would
//     need its own enrollment.
//   - An obligation param typed as an ALIAS of tenant.RowVisibility still resolves
//     to the same *types.Named via go/types, so aliasing does not bypass. Verified
//     by the GoodNoTenant / GoodWithTenant fixtures NOT being flagged.
//   - A method that takes tenant.RowVisibility at the obligation slot but ignores
//     it in the body is not caught here (body-level concern; covered by the
//     storetest conformance four-scope × subject matrix that asserts cross-subject
//     reads return empty / NotFound).
//   - A NEW carve-out entry for a method that is actually a row read would be a
//     silent bypass — guarded by the no-stale-carve-out check plus the rationale
//     requirement; reviewers must reject a carve-out whose method returns
//     subject-owned rows.
package archtest

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const (
	// ledgerStorePkg is the audit ledger ports package (Store interface). Derived
	// from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01) so a module rename
	// updates exactly one place.
	ledgerStorePkg = PlatformModulePath + "/runtime/audit/ledger"
	// rowScopeRepoParamFixPkg is a relative load path for go/packages — NOT a
	// platform import path — so it is intentionally not derived from PlatformModulePath.
	rowScopeRepoParamFixPkg = "./tools/archtest/internal/rowscoperepoparamfixture"
)

// rowScopedRepoIfaces are the row-scoped repo interfaces every (non-allowlisted)
// method of which must carry a tenant.RowVisibility obligation positional
// parameter. Looked up via Scope.Lookup in each enrolled ports package; absent
// names resolve to nil and are skipped, so the same list works across packages.
var rowScopedRepoIfaces = []string{
	// runtime/audit/ledger (ledgerStorePkg)
	"Store",
}

// rowScopeParamCarveOut is the non-row-read allowlist (see file godoc). Key is
// "<Interface>.<Method>".
var rowScopeParamCarveOut = map[string]string{
	"Store.Protocol":  "returns immutable protocol decisions; not a row read",
	"Store.Append":    "write path (persist an entry); not a row read",
	"Store.Tail":      "namespace-global chain-tail snapshot; not a subject-owned row read",
	"Store.Verify":    "HMAC chain integrity scan over [from,to]; not a subject-scoped read",
	"Store.RepoReady": "healthz relation probe; not a data read",
}

// enrolledRowScopePkgs is the closed set of ports packages whose repo interfaces
// are enrolled in ROWSCOPE-REPO-PARAM-FUNNEL-01.
var enrolledRowScopePkgs = map[string]struct{}{
	ledgerStorePkg: {},
}

// isRowVisibilityType reports whether t is pkg/tenant.RowVisibility (alias-proof
// via the resolved *types.Named identity). tenantPkgPath is declared in
// tenant_repo_param_funnel_test.go (same package).
func isRowVisibilityType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == tenantPkgPath && obj.Name() == "RowVisibility"
}

// rowVisibilityAtObligationSlot asserts the typed positional contract for
// row-scoped repo methods:
//
//   - param[0] must be context.Context;
//   - the obligation slot is param[2] when param[1] is tenant.TenantID, else
//     param[1]; that slot must be exactly tenant.RowVisibility.
//
// All checks use go/types object identity (isContextType / isTenantIDType /
// isRowVisibilityType), so import aliases and type aliases resolve correctly.
func rowVisibilityAtObligationSlot(sig *types.Signature) bool {
	params := sig.Params()
	if params.Len() < 2 {
		return false
	}
	if !isContextType(params.At(0).Type()) {
		return false
	}
	if isTenantIDType(params.At(1).Type()) {
		if params.Len() < 3 {
			return false
		}
		return isRowVisibilityType(params.At(2).Type())
	}
	return isRowVisibilityType(params.At(1).Type())
}

// scanRowScopeRepoParam inspects the named interfaces in p.Pkg and reports every
// explicit method that fails the obligation-slot assertion and is not
// allowlisted. withRowVis counts methods that DID satisfy it (anti-vacuity
// signal). seenMethods records every "<Iface>.<Method>" observed (stale-allowlist
// check).
func scanRowScopeRepoParam(
	p *Pass, ifaceNames []string, allow map[string]string,
) (diags []Diagnostic, withRowVis int, seenMethods map[string]struct{}) {
	seenMethods = map[string]struct{}{}
	scope := p.Pkg.Scope()
	for _, ifaceName := range ifaceNames {
		obj := scope.Lookup(ifaceName)
		if obj == nil {
			continue // not in this package (fixture vs production split)
		}
		iface, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}
		for i := 0; i < iface.NumExplicitMethods(); i++ {
			m := iface.ExplicitMethod(i)
			key := ifaceName + "." + m.Name()
			seenMethods[key] = struct{}{}
			if _, exempt := allow[key]; exempt {
				continue
			}
			sig, _ := m.Type().(*types.Signature)
			if sig == nil {
				continue
			}
			if rowVisibilityAtObligationSlot(sig) {
				withRowVis++
				continue
			}
			pos := p.Fset.Position(m.Pos())
			diags = append(diags, Diagnostic{
				Line: pos.Line,
				Message: fmt.Sprintf(
					"ROWSCOPE-REPO-PARAM-FUNNEL-01: %s.%s does not carry tenant.RowVisibility at the "+
						"obligation slot (param[1] after ctx, or param[2] after ctx+tenant.TenantID). "+
						"Row-scoped repo read methods MUST take the tenant.RowVisibility obligation as a "+
						"typed positional parameter so the row-visibility predicate is type-enforced at the "+
						"call site. If this is not a subject-owned row read, add %q to rowScopeParamCarveOut "+
						"with rationale.",
					ifaceName, m.Name(), key,
				),
			})
		}
	}
	return diags, withRowVis, seenMethods
}

// TestRowScopeRepoParamFunnel01 asserts every enrolled row-scoped repo interface
// method (minus the carve-out allowlist) carries a tenant.RowVisibility at the
// obligation slot, and that no carve-out entry is stale.
func TestRowScopeRepoParamFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		totalWithRowVis int
		allSeen         = map[string]struct{}{}
	)
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		if _, enrolled := enrolledRowScopePkgs[p.Pkg.Path()]; !enrolled {
			return nil
		}
		d, withRowVis, seen := scanRowScopeRepoParam(p, rowScopedRepoIfaces, rowScopeParamCarveOut)
		totalWithRowVis += withRowVis
		for k := range seen {
			allSeen[k] = struct{}{}
		}
		return d
	})

	// Anti-vacuity: the scan must have resolved real interfaces and observed at
	// least one method actually carrying the obligation (else a path/typing
	// regression would make this test vacuously pass).
	if len(allSeen) == 0 {
		diags = append(diags, Diagnostic{Message: "ROWSCOPE-REPO-PARAM-FUNNEL-01: scanned zero repo interface methods — " +
			"scanner regressed or enrolled ports package path changed (expected " + ledgerStorePkg + ")"})
	}
	if totalWithRowVis == 0 {
		diags = append(diags, Diagnostic{Message: "ROWSCOPE-REPO-PARAM-FUNNEL-01: zero methods carry a tenant.RowVisibility " +
			"parameter — go/types obligation-type resolution regressed, or no row-scoped read is enrolled"})
	}

	// No-stale carve-out: each allowlisted method must exist on a scanned interface.
	stale := make([]string, 0)
	for key := range rowScopeParamCarveOut {
		if _, seen := allSeen[key]; !seen {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		diags = append(diags, Diagnostic{Message: fmt.Sprintf(
			"ROWSCOPE-REPO-PARAM-FUNNEL-01: carve-out entry %q is STALE — no such interface method observed. "+
				"Drop the dead allowlist entry so it cannot become a silent bypass slot.", key,
		)})
	}

	Report(t, "ROWSCOPE-REPO-PARAM-FUNNEL-01", diags)
}

// TestRowScopeRepoParamFunnel01_ScannerCatchesViolation is the reverse self-check:
// it runs the SAME scanner against the RED fixture and asserts:
//   - GetByThing (no obligation param) is reported;
//   - WrongPosition (tenant.RowVisibility at the wrong position) is reported;
//   - GoodNoTenant (obligation at param[1]) and GoodWithTenant (obligation at
//     param[2] after TenantID) are NOT reported.
//
// Expects exactly 2 diagnostics (GetByThing + WrongPosition).
func TestRowScopeRepoParamFunnel01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{rowScopeRepoParamFixPkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		d, _, _ := scanRowScopeRepoParam(p, []string{"FakeRowScopedRepo"}, nil)
		return d
	})

	if len(diags) != 2 {
		t.Fatalf("expected exactly 2 diagnostics (GetByThing + WrongPosition), got %d: %v", len(diags), diags)
	}
	var msgs []string
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	sort.Strings(msgs)
	foundMissing := false
	foundWrongPos := false
	for _, m := range msgs {
		if strings.Contains(m, "FakeRowScopedRepo.GetByThing") {
			foundMissing = true
		}
		if strings.Contains(m, "FakeRowScopedRepo.WrongPosition") {
			foundWrongPos = true
		}
	}
	if !foundMissing {
		t.Errorf("expected diagnostic for FakeRowScopedRepo.GetByThing (missing obligation), got messages: %v", msgs)
	}
	if !foundWrongPos {
		t.Errorf("expected diagnostic for FakeRowScopedRepo.WrongPosition (wrong position), got messages: %v", msgs)
	}
}
