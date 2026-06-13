//go:build archtest

// rowscopeall_audit_funnel_test.go — closes the PRODUCER side of the
// "RowScope=all ⟹ mandatory cross-tenant audit" funnel (epic #1337 PR-5, #1343;
// Hard-upgraded #1760).
//
// INVARIANT: ROWSCOPEALL-AUDIT-FUNNEL-01
//
// # What this guards
//
// A RowScopeAll obligation grants cross-tenant visibility. As of #1760 it is
// produced ONLY by the sealed tenant.NewCrossTenantVisibility constructor —
// tenant.NewRowVisibility now REJECTS RowScopeAll (fail-closed KindInternal), so
// the general path is provably incapable of minting it. The single sanctioned
// mint of NewCrossTenantVisibility lives in (*auth.Principal).CrossTenantVisibility,
// whose body emits a mandatory slog.Error security event (spec FR-007)
// co-located with — and unconditionally before — the construction. RowVisibility's
// super-admin branch delegates to that accessor, so every cross-tenant obligation
// is audited regardless of which caller triggers it.
//
// This archtest pins every PRODUCTION call to tenant.NewCrossTenantVisibility to a
// bounded allowlist. Any non-allowlisted site that mints the obligation would
// bypass the co-located audit — re-opening the "silent cross-tenant read" hole. It
// is the producer-side complement to ROWSCOPE-REPO-PARAM-FUNNEL-01 (which makes the
// obligation a mandatory typed positional param + sealed, so a caller can neither
// forget nor forge it): that rule guarantees the store always RECEIVES an
// obligation; this rule guarantees a RowScopeAll obligation can only be PRODUCED by
// an audited / sanctioned site.
//
// # Legitimate producers (today)
//
//   - runtime/auth/rowscope.go — (*Principal).CrossTenantVisibility, the sole
//     super-admin mint, which slog.Error-audits the cross-tenant access before
//     constructing the obligation (RowVisibility delegates here).
//   - runtime/audit/ledger/storetest/suite.go — the ledger.Store conformance
//     suite, a testing-helper package (every callsite takes testing.TB) that
//     mints an All obligation to exercise the serving store's fail-closed path.
//
// _test.go files and the archtest fixture are also exempt: the Production scan
// excludes _test.go (Tests:false) and the build-tagged fixture.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - General-path closure: HARD. tenant.NewRowVisibility rejects RowScopeAll at
//     runtime (fail-closed), covering every laundering form at the actual call
//     (a var/param/return that resolves to All errors just the same). The sole
//     producer is the sealed NewCrossTenantVisibility; struct-literal forgery of
//     either RowVisibility{scope:All} or CrossTenantVisibility{} is a compile error
//     (unexported fields).
//   - Downstream: HARD. The #1810 cross-tenant audit read takes a
//     tenant.CrossTenantVisibility positional parameter (not a bare RowVisibility),
//     so the read is uncallable without routing through the sealed minter — forget
//     = compile error, forge = compile error.
//   - Minter caller-restriction: MEDIUM, a GO-LANGUAGE CEILING (not a deferred
//     TODO). "Only (*Principal).CrossTenantVisibility may call
//     NewCrossTenantVisibility" is not compile-time expressible: pkg/tenant cannot
//     import runtime/auth (cycle), and the constructor must stay exported for the
//     conformance suite. This archtest caller-allowlist is the enforcement. Same
//     permanent ceiling as CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (#1282) /
//     SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893). #1760 shipped
//     the Hard general-path closure + the sealed minter + the Hard downstream
//     funnel; this residual caller-restriction is the permanent Medium ceiling.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection is symbol-based (a tenant.NewCrossTenantVisibility CallExpr). The
//     constructor takes NO scope argument, so the prior scope-laundering blind spot
//     (var/param/return scope) is structurally eliminated — there is nothing to
//     launder. go/types resolution of the callee is alias-proof (qualified,
//     aliased, dot-imported).
//   - tenant.NewRowVisibility is NOT scanned here: it can no longer mint All (it
//     errors at runtime), so a NewRowVisibility(RowScopeAll, …) call is dead code,
//     not a cross-tenant hole. The runtime rejection + the pkg/tenant unit test are
//     the enforcement for that path.
//   - The anti-vacuity guard (every allowlist entry must be observed ≥1× as a live
//     NewCrossTenantVisibility call) is the reverse self-check: it forbids a stale
//     entry becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// rowScopeAllProducerAllowlist is the set of module-relative production files
// allowed to call tenant.NewCrossTenantVisibility (the sole RowScopeAll
// producer). See the file godoc.
var rowScopeAllProducerAllowlist = map[string]struct{}{
	"runtime/auth/rowscope.go":                {}, // (*Principal).CrossTenantVisibility — slog.Error-audits first
	"runtime/audit/ledger/storetest/suite.go": {}, // ledger.Store conformance suite — testing-helper pkg
}

// rowScopeAllAuditFixturePkg is the build-tagged RED fixture package exercised
// by the reverse self-check.
const rowScopeAllAuditFixturePkg = "./tools/archtest/internal/rowscopeallauditfixture"

// TestRowScopeAllAuditFunnel01 asserts every production tenant.NewCrossTenantVisibility
// call sits in rowScopeAllProducerAllowlist, and that every allowlist entry is live
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
	// a live NewCrossTenantVisibility construction, else it is a dead bypass slot.
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
						"tenant.NewCrossTenantVisibility construction observed. Either the scanner "+
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
// a parallel counter that could silently drift (#1759 F2). It asserts the detector
// flags EXACTLY the fixture's NewCrossTenantVisibility mint (MintCrossTenantOutsideFunnel)
// and NOT the GREEN control (MintSelf), and that an allowlisted run suppresses it on
// the same path.
func TestRowScopeAllAuditFunnel01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Empty allowlist: the fixture's NewCrossTenantVisibility mint is flagged.
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{rowScopeAllAuditFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkRowScopeAllAuditFunnel(p, nil)
		return d
	})

	const wantFlagged = 1 // MintCrossTenantOutsideFunnel (MintSelf is the GREEN control)
	if len(diags) != wantFlagged {
		t.Fatalf("ROWSCOPEALL-AUDIT-FUNNEL-01 scanner self-check: expected the production detector to "+
			"flag exactly the %d NewCrossTenantVisibility fixture mint and NOT the RowScopeSelf control, "+
			"got %d: %+v", wantFlagged, len(diags), diags)
	}
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
// tenant.NewCrossTenantVisibility calls, flagging any not in allowlist. It returns
// the diagnostics plus the set of module-relative files in which a construction was
// observed (for the anti-vacuity reverse check). It is the SINGLE detection path
// shared by the production test and the fixture self-check (#1759 F2), so the
// fixture exercises the exact allowlist + Diagnostic logic and detector drift cannot
// pass the self-check.
func checkRowScopeAllAuditFunnel(p *Pass, allowlist map[string]struct{}) (diags []Diagnostic, observed map[string]struct{}) {
	observed = map[string]struct{}{}
	if !p.Typed() {
		return nil, observed
	}
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if !IsCallToPkgFunc(p.TypesInfo, call, tenantPkgPath, "NewCrossTenantVisibility") {
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
					"ROWSCOPEALL-AUDIT-FUNNEL-01: tenant.NewCrossTenantVisibility in %s mints a RowScopeAll "+
						"(cross-tenant) obligation. It MUST be minted only by the audited "+
						"(*auth.Principal).CrossTenantVisibility derivation, which slog.Error-audits the "+
						"cross-tenant access first (spec FR-007). Derive the obligation via "+
						"Principal.CrossTenantVisibility; or, if this IS a sanctioned cross-scope producer "+
						"(e.g. a conformance helper), add it to rowScopeAllProducerAllowlist with a rationale "+
						"and co-locate the mandatory audit.",
					rel,
				),
			})
		})
	}
	return diags, observed
}
