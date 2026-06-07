// rowscopeall_audit_funnel_test.go — closes the PRODUCER side of the
// "RowScope=all ⟹ mandatory cross-tenant audit" funnel (epic #1337 PR-5, #1343).
//
//   - INVARIANT: ROWSCOPEALL-AUDIT-FUNNEL-01
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
// This archtest pins every PRODUCTION call to tenant.NewRowVisibility whose
// scope argument resolves to tenant.RowScopeAll to a bounded allowlist
// (runtime/auth/rowscope.go). Any other site minting a RowScopeAll obligation
// would bypass the co-located audit — re-opening the "silent cross-tenant read"
// hole. It is the producer-side complement to ROWSCOPE-REPO-PARAM-FUNNEL-01
// (which makes the obligation a mandatory typed positional param + sealed, so a
// caller can neither forget nor forge it): that rule guarantees the store always
// RECEIVES an obligation; this rule guarantees a RowScopeAll obligation can only
// be PRODUCED by the audited derivation.
//
// # Legitimate producers (today)
//
//   - runtime/auth/rowscope.go — (*Principal).RowVisibility, the super-admin
//     branch, which slog.Error-audits the cross-tenant access before constructing
//     the all obligation. Test files and the archtest fixture are exempt (they
//     construct RowScopeAll to exercise the store PEP / the scanner itself); the
//     Production scan excludes _test.go and the build-tagged fixture.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD. ROWSCOPE-REPO-PARAM-FUNNEL-01 already makes the
//     obligation a typed positional param (forget = compile error) and sealed
//     (forge by struct literal = compile error), so the store cannot be reached
//     with an all obligation that did not come from NewRowVisibility.
//   - Upstream: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Hard
//     upstream would require RowScopeAll construction to be unreachable outside
//     the audited derivation; tenant.NewRowVisibility must stay exported (the
//     store conformance suite, fixtures, and the future PR-11/12 obligation
//     combiner construct obligations across packages), and Go visibility cannot
//     express "only this one func may pass RowScopeAll to an exported
//     constructor". Same permanent ceiling as CTXKEYS-PRINCIPAL-WRITE-CALLER-01
//     (#1282) / SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893).
//     The Hard upgrade — a separate sealed all-construction type — is tracked as
//     a deliberate won't-do-now (gh issue registered with this PR). The
//     downstream typed/sealed param + the co-located mandatory audit are the
//     enforcement.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection resolves call.Args[0] via ResolvePackageRef, so a caller that
//     launders RowScopeAll through a local variable/alias (`s := tenant.RowScopeAll;
//     NewRowVisibility(s, "")`) is NOT const-resolved and would be missed — the
//     same non-const-arg ceiling as the saga metric-label funnel. The idiomatic
//     mint passes the const directly; the reverse fixture proves the direct-const
//     form is caught and the RowScopeSelf control is not.
//   - Detection is call-based (tenant.NewRowVisibility CallExpr). A second
//     constructor for RowVisibility would need adding to the scan; today
//     NewRowVisibility is the sole constructor (sealed — see ROWSCOPE-REPO-PARAM
//     godoc + pkg/tenant/rowvisibility.go).
//   - The anti-vacuity guard (the allowlist entry must be observed ≥1×) is the
//     reverse self-check: it proves the scanner resolves the real RowScopeAll
//     mint and forbids a stale entry becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"
)

// rowScopeAllProducerAllowlist is the set of module-relative production files
// allowed to construct a tenant.RowScopeAll obligation. See the file godoc.
var rowScopeAllProducerAllowlist = map[string]struct{}{
	"runtime/auth/rowscope.go": {}, // (*Principal).RowVisibility super-admin branch — slog.Error-audits first
}

// rowScopeAllAuditFixturePkg is the build-tagged RED fixture package exercised
// by the reverse self-check.
const rowScopeAllAuditFixturePkg = "./tools/archtest/internal/rowscopeallauditfixture"

// TestRowScopeAllAuditFunnel01 asserts every production NewRowVisibility call
// whose scope arg is tenant.RowScopeAll sits in rowScopeAllProducerAllowlist,
// and that the allowlist entry is live (anti-vacuity reverse check).
func TestRowScopeAllAuditFunnel01(t *testing.T) {
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
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isNewRowVisibilityAllCall(p.TypesInfo, call) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := rowScopeAllProducerAllowlist[rel]; !allowed {
					pos := p.Fset.Position(call.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"ROWSCOPEALL-AUDIT-FUNNEL-01: tenant.NewRowVisibility(tenant.RowScopeAll, …) is "+
								"constructed in %s, which is not a sanctioned producer. A RowScopeAll obligation "+
								"grants cross-tenant visibility and MUST be minted only by the audited "+
								"(*auth.Principal).RowVisibility derivation, which slog.Error-audits the "+
								"cross-tenant access first (spec FR-007). Minting it elsewhere bypasses the "+
								"mandatory audit. Derive the obligation via Principal.RowVisibility; or if this IS "+
								"a new sanctioned producer, add it to rowScopeAllProducerAllowlist with a rationale "+
								"and co-locate the mandatory audit.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlist entry must host
	// a live RowScopeAll construction, else it is a dead bypass slot.
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
						"tenant.NewRowVisibility(tenant.RowScopeAll, …) construction observed. Either the "+
						"scanner regressed or the audited derivation was removed; drop the dead allowlist "+
						"entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "ROWSCOPEALL-AUDIT-FUNNEL-01", diags)
}

// TestRowScopeAllAuditFunnel01_ScannerCatchesViolation is the reverse self-check:
// it runs the SAME detector against the RED fixture and asserts EXACTLY the
// out-of-allowlist RowScopeAll construction is flagged (len==1) — proving the
// detector catches the all mint AND does not false-positive on the fixture's
// RowScopeSelf control.
func TestRowScopeAllAuditFunnel01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var flagged int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{rowScopeAllAuditFixturePkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if isNewRowVisibilityAllCall(p.TypesInfo, call) {
					flagged++
				}
			})
		}
		return nil
	})

	if flagged != 1 {
		t.Fatalf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: expected to flag exactly the RED "+
			"fixture's one NewRowVisibility(RowScopeAll, …) call (and not the RowScopeSelf control), "+
			"got %d — the detector regressed", flagged)
	}
}

// isNewRowVisibilityAllCall reports whether call is tenant.NewRowVisibility(…)
// with its first (scope) argument resolving to tenant.RowScopeAll, alias-proof
// via go/types (ResolvePackageRef resolves a qualified `tenant.RowScopeAll` .Sel
// and a dot-imported bare `RowScopeAll` to the same package-level const).
func isNewRowVisibilityAllCall(info *types.Info, call *ast.CallExpr) bool {
	if !IsCallToPkgFunc(info, call, tenantPkgPath, "NewRowVisibility") {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(info, call.Args[0])
	return ok && pkgPath == tenantPkgPath && name == "RowScopeAll"
}
