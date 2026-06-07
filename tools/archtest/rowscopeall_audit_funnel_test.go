// rowscopeall_audit_funnel_test.go — closes the PRODUCER side of the
// "RowScope=all ⟹ mandatory cross-tenant audit" funnel (epic #1337 PR-5, #1343).
//
// INVARIANT: ROWSCOPEALL-AUDIT-FUNNEL-01
//
// # What this guards
//
// PR-5 un-fail-closes tenant.RowScopeAll at the audit ledger stores (the store
// is a pure PEP: it APPLIES the obligation, it does not decide who may hold it).
// The security invariant "every cross-tenant (RowScopeAll) read is audited"
// (spec FR-007) then rests entirely on the obligation PRODUCER: the sole site
// that mints a RowScopeAll RowVisibility is (*auth.Principal).RowVisibility,
// whose super-admin branch emits a mandatory slog.Error security event
// co-located with — and unconditionally before — the
// NewRowVisibility(RowScopeAll, "") construction.
//
// This archtest pins every PRODUCTION call to tenant.NewRowVisibility that the
// scanner cannot prove is non-All to a bounded allowlist. It is FAIL-CLOSED
// (#1759 F1): a call is flagged when its scope argument is RowScopeAll OR is not
// a compile-time constant the scanner can prove is non-All (laundered through a
// var/param/return). Any non-allowlisted site that could mint a RowScopeAll
// obligation would bypass the co-located audit — re-opening the "silent
// cross-tenant read" hole. It is the producer-side complement to
// ROWSCOPE-REPO-PARAM-FUNNEL-01 (which makes the obligation a mandatory typed
// positional param + sealed, so a caller can neither forget nor forge it): that
// rule guarantees the store always RECEIVES an obligation; this rule guarantees
// a RowScopeAll obligation can only be PRODUCED by an audited / sanctioned site.
//
// # Legitimate producers (today)
//
//   - runtime/auth/rowscope.go — (*Principal).RowVisibility, the super-admin
//     branch, which slog.Error-audits the cross-tenant access before
//     constructing the all obligation.
//   - runtime/audit/ledger/storetest/suite.go — the ledger.Store conformance
//     suite, a testing-helper package (every callsite takes testing.TB) that
//     constructs RowVisibility across ALL four scopes (including RowScopeAll for
//     the cross-tenant conformance case) from a parametric `scope` argument.
//     The parametric mint is fail-closed (non-constant arg), so it is
//     explicitly sanctioned here rather than silently passed.
//
// _test.go files and the archtest fixture are also exempt (they construct
// RowScopeAll to exercise the store PEP / the scanner itself): the Production
// scan excludes _test.go (Tests:false) and the build-tagged fixture.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD. ROWSCOPE-REPO-PARAM-FUNNEL-01 already makes the
//     obligation a typed positional param (forget = compile error) and sealed
//     (forge by struct literal = compile error), so the store cannot be reached
//     with an all obligation that did not come from NewRowVisibility. This
//     archtest's own detection is now fail-closed (go/types constant folding,
//     alias-proof), so the AST detection has no laundering blind spot.
//   - Upstream: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Hard
//     upstream would require RowScopeAll construction to be unreachable outside
//     the audited derivation; tenant.NewRowVisibility must stay exported (the
//     store conformance suite, fixtures, and the future PR-11/12 obligation
//     combiner construct obligations across packages), and Go visibility cannot
//     express "only this one func may pass RowScopeAll to an exported
//     constructor". Same permanent ceiling as CTXKEYS-PRINCIPAL-WRITE-CALLER-01
//     (#1282) / SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893).
//     The Hard upgrade — a separate sealed all-construction type — is tracked as
//     a deliberate won't-do-now at **gh #1760**. The downstream typed/sealed
//     param + the co-located mandatory audit + the fail-closed allowlist are the
//     enforcement.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection is FAIL-CLOSED over the scope argument's compile-time constant
//     value (info.Types[arg].Value vs the resolved value of tenant.RowScopeAll).
//     A call whose scope arg is RowScopeAll (direct const, dot-imported bare
//     ident, local const alias that folds to All, or untyped literal 4) is
//     flagged; a call whose scope arg is NOT a compile-time constant (local
//     var, parameter, function return) is flagged too (cannot prove non-All).
//     Only a scope arg that is a compile-time constant provably != RowScopeAll
//     (RowScopeSelf/Device/Tenant) passes. The previous direct-const-only
//     detector (ResolvePackageRef on call.Args[0]) silently passed the
//     var/param laundering form; the RED fixture (MintAllViaLocalVar /
//     MintAllViaLocalConst) is the reverse self-check that the fail-closed
//     detector now catches both, and the GREEN controls (MintSelf /
//     MintSelfViaLocalConst) prove it does not over-flag a provable non-All
//     constant.
//   - Detection is call-based (tenant.NewRowVisibility CallExpr). A second
//     constructor for RowVisibility would need adding to the scan; today
//     NewRowVisibility is the sole constructor (sealed — see ROWSCOPE-REPO-PARAM
//     godoc + pkg/tenant/rowvisibility.go).
//   - The anti-vacuity guard (every allowlist entry must be observed ≥1× as a
//     live fail-closed construction) is the reverse self-check: it proves the
//     scanner resolves the real RowScopeAll / parametric mint and forbids a
//     stale entry becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"
)

// rowScopeAllProducerAllowlist is the set of module-relative production files
// allowed to construct a tenant.RowScopeAll obligation (or to pass a
// non-provable scope to NewRowVisibility). See the file godoc.
var rowScopeAllProducerAllowlist = map[string]struct{}{
	"runtime/auth/rowscope.go":                {}, // (*Principal).RowVisibility super-admin branch — slog.Error-audits first
	"runtime/audit/ledger/storetest/suite.go": {}, // ledger.Store conformance suite — parametric all-scope mint (testing-helper pkg)
}

// rowScopeAllAuditFixturePkg is the build-tagged RED fixture package exercised
// by the reverse self-check.
const rowScopeAllAuditFixturePkg = "./tools/archtest/internal/rowscopeallauditfixture"

// TestRowScopeAllAuditFunnel01 asserts every production NewRowVisibility call
// whose scope arg is RowScopeAll — or that the scanner cannot prove is non-All —
// sits in rowScopeAllProducerAllowlist, and that every allowlist entry is live
// (anti-vacuity reverse check).
func TestRowScopeAllAuditFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		d, obs := checkRowScopeAllAuditFunnel(p, rowScopeAllProducerAllowlist)
		for f := range obs {
			observed[f] = struct{}{}
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlist entry must host
	// a live fail-closed construction, else it is a dead bypass slot.
	allowed := make([]string, 0, len(rowScopeAllProducerAllowlist))
	for f := range rowScopeAllProducerAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"ROWSCOPEALL-AUDIT-FUNNEL-01: allowlist entry %q is STALE — no live "+
						"fail-closed tenant.NewRowVisibility construction observed. Either the scanner "+
						"regressed or the sanctioned producer was removed; drop the dead allowlist entry "+
						"so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "ROWSCOPEALL-AUDIT-FUNNEL-01", diags)
}

// TestRowScopeAllAuditFunnel01_ScannerCatchesViolation is the reverse self-check:
// it runs the SAME production detector (checkRowScopeAllAuditFunnel) against the
// RED fixture — exercising the real allowlist + Diagnostic construction path, not
// a parallel counter that could silently drift (#1759 F2). It asserts the
// detector flags EXACTLY the fixture's fail-closed constructions (MintAll +
// MintAllViaLocalVar + MintAllViaLocalConst) and NOT the GREEN controls (MintSelf
// + MintSelfViaLocalConst), and that an allowlisted run suppresses them on the
// same path.
func TestRowScopeAllAuditFunnel01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Empty allowlist: every fail-closed construction in the fixture is flagged.
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{rowScopeAllAuditFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkRowScopeAllAuditFunnel(p, nil)
		return d
	})

	const wantFlagged = 3 // MintAll, MintAllViaLocalVar, MintAllViaLocalConst
	if len(diags) != wantFlagged {
		t.Fatalf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: expected the production detector to "+
			"flag exactly the %d fail-closed fixture constructions (direct + var-laundered + "+
			"const-laundered RowScopeAll) and NOT the RowScopeSelf controls, got %d: %+v",
			wantFlagged, len(diags), diags)
	}
	lines := map[int]struct{}{}
	for _, d := range diags {
		if !strings.HasSuffix(d.Rel, "rowscopeallauditfixture/fixture.go") {
			t.Errorf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: diagnostic Rel %q is not the RED "+
				"fixture file", d.Rel)
		}
		if !strings.Contains(d.Message, "ROWSCOPEALL-AUDIT-FUNNEL-01") {
			t.Errorf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: diagnostic missing rule ID: %q", d.Message)
		}
		if d.Line <= 0 {
			t.Errorf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: diagnostic has no resolved line: %+v", d)
		}
		lines[d.Line] = struct{}{}
	}
	if len(lines) != wantFlagged {
		t.Errorf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: expected %d distinct flagged callsites, "+
			"got %d (lines=%v) — detector may be flagging one callsite twice or missing one", wantFlagged, len(lines), lines)
	}

	// Same detector, fixture file IN the allowlist → ZERO diagnostics. Proves the
	// production allowlist-suppression branch is exercised on the identical path.
	if len(diags) == 0 {
		return // already failed above; avoid index panic
	}
	allowFixture := map[string]struct{}{diags[0].Rel: {}}
	suppressed := Run(t, Fixture(FixtureOpts{Tests: false}, []string{rowScopeAllAuditFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkRowScopeAllAuditFunnel(p, allowFixture)
		return d
	})
	if len(suppressed) != 0 {
		t.Fatalf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: allowlisting the fixture file must "+
			"suppress all diagnostics on the same detector path, got %d: %+v", len(suppressed), suppressed)
	}
}

// checkRowScopeAllAuditFunnel scans one typed Pass for production
// tenant.NewRowVisibility calls whose scope argument is RowScopeAll or cannot be
// proven non-All (fail-closed), flagging any not in allowlist. It returns the
// diagnostics plus the set of module-relative files in which a fail-closed
// construction was observed (for the anti-vacuity reverse check). It is the
// SINGLE detection path shared by the production test and the fixture self-check
// (#1759 F2), so the fixture exercises the exact allowlist + Diagnostic logic and
// detector drift cannot pass the self-check.
func checkRowScopeAllAuditFunnel(p *Pass, allowlist map[string]struct{}) (diags []Diagnostic, observed map[string]struct{}) {
	observed = map[string]struct{}{}
	if !p.Typed() {
		return nil, observed
	}
	allVal := tenantRowScopeAllValue(p)
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if !isFailClosedNewRowVisibilityCall(p.TypesInfo, allVal, call) {
				return
			}
			observed[rel] = struct{}{}
			if _, allowed := allowlist[rel]; allowed {
				return
			}
			pos := p.Fset.Position(call.Pos())
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"ROWSCOPEALL-AUDIT-FUNNEL-01: tenant.NewRowVisibility in %s is constructed with a scope "+
						"argument that is RowScopeAll, or that the scanner cannot prove is a non-All compile-time "+
						"constant (laundered through a var/param/return — fail-closed). A RowScopeAll obligation "+
						"grants cross-tenant visibility and MUST be minted only by the audited "+
						"(*auth.Principal).RowVisibility derivation, which slog.Error-audits the cross-tenant "+
						"access first (spec FR-007). Derive the obligation via Principal.RowVisibility; pass a "+
						"compile-time non-All scope constant directly elsewhere; or, if this IS a sanctioned "+
						"cross-scope producer (e.g. a conformance helper), add it to rowScopeAllProducerAllowlist "+
						"with a rationale and co-locate the mandatory audit.",
					rel,
				),
			})
		})
	}
	return diags, observed
}

// isFailClosedNewRowVisibilityCall reports whether call is tenant.NewRowVisibility
// whose first (scope) argument is RowScopeAll OR is not a compile-time constant
// the scanner can prove is non-All. It is fail-closed: a non-constant scope
// (local var / parameter / function return) returns true because the scanner
// cannot prove it isn't RowScopeAll at runtime. A constant scope returns true
// only when its value equals tenant.RowScopeAll (constant folding resolves a
// direct const, a dot-imported bare ident, a local const alias, and an untyped
// literal). go/types resolution is alias-proof.
func isFailClosedNewRowVisibilityCall(info *types.Info, allVal constant.Value, call *ast.CallExpr) bool {
	if !IsCallToPkgFunc(info, call, tenantPkgPath, "NewRowVisibility") {
		return false
	}
	if len(call.Args) == 0 {
		return true // malformed call — fail closed
	}
	if allVal == nil {
		return true // could not resolve tenant.RowScopeAll's value — fail closed
	}
	tv, ok := info.Types[call.Args[0]]
	if !ok || tv.Value == nil {
		// scope arg is not a compile-time constant (var/param/return); cannot
		// prove it is non-All. Fail closed.
		return true
	}
	return constant.Compare(tv.Value, token.EQL, allVal)
}

// tenantRowScopeAllValue resolves the compile-time constant value of
// tenant.RowScopeAll via the Pass's imported tenant package, or nil if it cannot
// be resolved (treated as fail-closed by the caller). Any package that calls
// tenant.NewRowVisibility imports pkg/tenant directly (incl. dot-import), so the
// const is reachable from p.Pkg.Imports().
func tenantRowScopeAllValue(p *Pass) constant.Value {
	if p.Pkg == nil {
		return nil
	}
	for _, imp := range p.Pkg.Imports() {
		if imp.Path() != tenantPkgPath {
			continue
		}
		c, ok := imp.Scope().Lookup("RowScopeAll").(*types.Const)
		if !ok {
			return nil
		}
		return c.Val()
	}
	return nil
}
