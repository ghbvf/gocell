//go:build archtest

// system_rowvisibility_callsite_test.go — pins the production callsites of
// tenant.SystemRowVisibility to a bounded sanctioned-caller allowlist (#1709).
//
// INVARIANT: SYSTEM-ROWVISIBILITY-CALLSITE-01
//
// # What this guards
//
// tenant.SystemRowVisibility() returns the tenant-wide (no owner predicate)
// obligation for trusted SYSTEM reads — credential / identity lookups, rbac
// enforcement, admin provisioning, credential-mutation re-fetch — reads that are
// scoped by tenant but carry NO subject-owner dimension. On a SUBJECT-FACING read
// path it is a footgun: it silently removes the owner predicate, so the row-scope
// PEP (the only defense-in-depth control on the data layer) is bypassed and every
// row in the tenant leaks to any authenticated caller who clears the coarse PDP
// route gate. Subject-self / device endpoints MUST instead derive the obligation
// from the authenticated principal via auth.Principal.RowVisibility(ctx).
//
// This archtest freezes the set of production files allowed to call
// tenant.SystemRowVisibility. A NEW callsite — most dangerously a subject-facing
// handler/service that should have derived from the principal — lands outside the
// allowlist and fails CI, forcing the author to justify the read is genuinely
// SYSTEM-scoped (and move it, or thread a principal-derived obligation instead).
// It is the callsite complement to ROWSCOPE-REPO-PARAM-FUNNEL-01 (which guarantees
// the repo always RECEIVES an obligation) and the FOOTGUN godoc on
// SystemRowVisibility (which states the rule prose); this rule makes the rule
// machine-checked.
//
// # Sanctioned callers (today)
//
// Subject-self read paths derive the principal obligation in the HANDLER and
// thread it into the SERVICE as the typed vis param — so the accesscore handlers
// are intentionally NOT in this allowlist (a handler that calls SystemRowVisibility
// is exactly the bug this rule catches). The allowlisted files call it only on
// SYSTEM-scoped methods that carry no end-user subject:
//
//   - slices/{sessionlogin,sessionrefresh,sessionvalidate}/service.go — credential
//     / identity re-fetch on the login / refresh / validate paths.
//   - slices/identitymanage/service.go — credential-mutation re-fetch
//     (ChangePassword / admin provisioning) and the last-admin-role guard.
//   - slices/rbacassign/service.go — role-assign target-existence re-fetch.
//   - internal/sessionmint/sessionmint.go — role read while minting session claims.
//   - internal/mem/user_repo.go, internal/adapters/postgres/user_repo.go — the
//     repos' own system-scoped GetByID convenience read.
//   - internal/ports/conformance/conformance.go, accesscoretest/fixture.go —
//     testing-helper packages (every callsite takes testing.TB; non-_test.go homes
//     so external Cell repos can compile them). Same testing-helper rationale as the
//     ROWSCOPEALL-AUDIT-FUNNEL-01 storetest / conformance allowlist entries.
//
// _test.go files and the archtest fixture are exempt: the Production scan excludes
// _test.go (Tests:false) and the build-tagged fixture.
//
// # AI-robust rating (charter §"分级")
//
//   - MEDIUM, a type-aware archtest scan. Detection resolves the callee via
//     go/types (tenant.SystemRowVisibility CallExpr), so qualified / aliased /
//     dot-imported forms are all caught; the obligation type itself is Hard-sealed
//     elsewhere (unexported fields → no struct-literal forgery). The residual Go
//     cannot close is granularity: the allowlist is FILE-level, so a NEW method
//     added to an already-allowlisted file (e.g. a subject-self method wrongly
//     calling SystemRowVisibility inside identitymanage/service.go) is not caught —
//     same permanent file-granularity ceiling as ROWSCOPEALL-AUDIT-FUNNEL-01. The
//     highest-risk surface (handlers serving subject-self endpoints) is fully
//     covered because no handler file is allowlisted.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection is symbol-based (a tenant.SystemRowVisibility CallExpr) and
//     alias-proof via go/types. The function takes NO arguments, so there is no
//     scope/argument laundering form to miss.
//   - File-level granularity (see rating): an unsanctioned call from WITHIN an
//     allowlisted file is not distinguished. Documented permanent ceiling.
//   - The anti-vacuity guard (every allowlist entry must be observed ≥1× as a live
//     SystemRowVisibility call) forbids a stale entry becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// systemRowVisibilityCallerAllowlist is the set of module-relative production
// files allowed to call tenant.SystemRowVisibility (the tenant-wide system-read
// obligation). See the file godoc § Sanctioned callers.
var systemRowVisibilityCallerAllowlist = map[string]struct{}{
	// SYSTEM-scoped service reads (no end-user subject dimension).
	"corecells/accesscore/slices/sessionlogin/service.go":      {}, // login credential lookup
	"corecells/accesscore/slices/sessionrefresh/service.go":    {}, // refresh re-fetch
	"corecells/accesscore/slices/sessionvalidate/service.go":   {}, // validate re-fetch
	"corecells/accesscore/slices/identitymanage/service.go":    {}, // credential-mutation re-fetch + last-admin guard
	"corecells/accesscore/slices/rbacassign/service.go":        {}, // role-assign target existence re-fetch
	"corecells/accesscore/internal/sessionmint/sessionmint.go": {}, // role read while minting session claims
	// Repo internal system-scoped GetByID convenience reads.
	"corecells/accesscore/internal/mem/user_repo.go":               {}, // mem GetByID system helper
	"corecells/accesscore/internal/adapters/postgres/user_repo.go": {}, // PG GetByID system re-fetch (write tx)
	// Testing-helper packages (every callsite takes testing.TB).
	"corecells/accesscore/internal/ports/conformance/conformance.go": {}, // repo conformance suite
	"corecells/accesscore/accesscoretest/fixture.go":                 {}, // accesscore test fixture
}

// systemRowVisibilityFixturePkg is the build-tagged RED fixture package exercised
// by the reverse self-check.
const systemRowVisibilityFixturePkg = "./tools/archtest/internal/systemrowvisibilityfixture"

// TestSystemRowVisibilityCallsite01 asserts every production
// tenant.SystemRowVisibility call sits in systemRowVisibilityCallerAllowlist, and
// that every allowlist entry is live (anti-vacuity reverse check).
func TestSystemRowVisibilityCallsite01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		d, obs := checkSystemRowVisibilityCallsite(p, systemRowVisibilityCallerAllowlist)
		for f := range obs {
			observed[f] = struct{}{}
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlist entry must host
	// a live SystemRowVisibility call, else it is a dead bypass slot.
	allowed := make([]string, 0, len(systemRowVisibilityCallerAllowlist))
	for f := range systemRowVisibilityCallerAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"SYSTEM-ROWVISIBILITY-CALLSITE-01: allowlist entry %q is STALE — no live "+
						"tenant.SystemRowVisibility call observed. Either the scanner regressed or the "+
						"sanctioned caller was removed; drop the dead allowlist entry so it cannot become "+
						"a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "SYSTEM-ROWVISIBILITY-CALLSITE-01", diags)
}

// TestSystemRowVisibilityCallsite01_ScannerCatchesViolation is the reverse
// self-check: it runs the SAME production detector (checkSystemRowVisibilityCallsite)
// against the RED fixture — exercising the real allowlist + Diagnostic construction
// path, not a parallel counter that could silently drift. It asserts the detector
// flags EXACTLY the fixture's SystemRowVisibility call (CallSystemRowVisibilityOutsideAllowlist)
// and NOT the GREEN control (DeriveNonSystem), and that an allowlisted run suppresses
// it on the same path.
func TestSystemRowVisibilityCallsite01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Empty allowlist: the fixture's SystemRowVisibility call is flagged.
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{systemRowVisibilityFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkSystemRowVisibilityCallsite(p, nil)
		return d
	})

	const wantFlagged = 1 // CallSystemRowVisibilityOutsideAllowlist (DeriveNonSystem is the GREEN control)
	if len(diags) != wantFlagged {
		t.Fatalf("SYSTEM-ROWVISIBILITY-CALLSITE-01 scanner self-check: expected the production detector to "+
			"flag exactly the %d SystemRowVisibility fixture call and NOT the RowScopeSelf control, "+
			"got %d: %+v", wantFlagged, len(diags), diags)
	}
	for _, d := range diags {
		if !strings.HasSuffix(d.Rel, "systemrowvisibilityfixture/fixture.go") {
			t.Errorf("SYSTEM-ROWVISIBILITY-CALLSITE-01 scanner self-check: diagnostic Rel %q is not the RED "+
				"fixture file", d.Rel)
		}
		if !strings.Contains(d.Message, "SYSTEM-ROWVISIBILITY-CALLSITE-01") {
			t.Errorf("SYSTEM-ROWVISIBILITY-CALLSITE-01 scanner self-check: diagnostic missing rule ID: %q", d.Message)
		}
		if d.Line <= 0 {
			t.Errorf("SYSTEM-ROWVISIBILITY-CALLSITE-01 scanner self-check: diagnostic has no resolved line: %+v", d)
		}
	}

	// Same detector, fixture file IN the allowlist → ZERO diagnostics. Proves the
	// production allowlist-suppression branch is exercised on the identical path.
	if len(diags) == 0 {
		return // already failed above; avoid index panic
	}
	allowFixture := map[string]struct{}{diags[0].Rel: {}}
	suppressed := Run(t, Fixture(FixtureOpts{Tests: false}, []string{systemRowVisibilityFixturePkg}), func(p *Pass) []Diagnostic {
		d, _ := checkSystemRowVisibilityCallsite(p, allowFixture)
		return d
	})
	if len(suppressed) != 0 {
		t.Fatalf("SYSTEM-ROWVISIBILITY-CALLSITE-01 scanner self-check: allowlisting the fixture file must "+
			"suppress all diagnostics on the same detector path, got %d: %+v", len(suppressed), suppressed)
	}
}

// checkSystemRowVisibilityCallsite scans one typed Pass for production
// tenant.SystemRowVisibility calls, flagging any not in allowlist. It returns the
// diagnostics plus the set of module-relative files in which a call was observed
// (for the anti-vacuity reverse check). It is the SINGLE detection path shared by
// the production test and the fixture self-check, so the fixture exercises the
// exact allowlist + Diagnostic logic and detector drift cannot pass the self-check.
func checkSystemRowVisibilityCallsite(p *Pass, allowlist map[string]struct{}) (diags []Diagnostic, observed map[string]struct{}) {
	observed = map[string]struct{}{}
	if !p.Typed() {
		return nil, observed
	}
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if !IsCallToPkgFunc(p.TypesInfo, call, tenantPkgPath, "SystemRowVisibility") {
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
					"SYSTEM-ROWVISIBILITY-CALLSITE-01: tenant.SystemRowVisibility in %s mints the tenant-wide "+
						"(no owner predicate) obligation. On a subject-facing read path this silently removes "+
						"the owner predicate and leaks every row in the tenant. Derive the obligation from the "+
						"authenticated principal via auth.Principal.RowVisibility(ctx); or, if this IS a "+
						"sanctioned SYSTEM read (no end-user subject dimension), add it to "+
						"systemRowVisibilityCallerAllowlist with a rationale.",
					rel,
				),
			})
		})
	}
	return diags, observed
}
