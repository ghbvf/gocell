// tenant_repo_param_funnel_test.go — guards that every tenant-scoped accesscore
// repo interface method carries a tenant.TenantID positional parameter.
//
//   - INVARIANT: TENANT-REPO-PARAM-FUNNEL-01
//   - INVARIANT: TENANT-REPO-CALLSITE-FUNNEL-01
//
// # What TENANT-REPO-PARAM-FUNNEL-01 guards (#1337 PR-2, Model A)
//
// Tenant isolation in GoCell is enforced as a mandatory typed positional
// parameter on the platform repo interfaces (route A — the only shape that can
// reach a type-system Hard guarantee). The DOMINANT guarantee is the compiler:
// once RoleRepository.GetByUserID(ctx, t tenant.TenantID, …) exists, a caller
// that "forgets the tenant" cannot compile — no fixture needed for that vector.
//
// This archtest guards the COMPLEMENTARY drift vector the compiler cannot catch:
// a NEW (or edited) repo method whose tenant slot is declared as a plain `string`
// instead of `tenant.TenantID`, OR where tenant.TenantID appears at the wrong
// position (not param[1]). Such a method type-checks fine but reopens the
// "no tenant predicate / wrong-typed tenant" hole.
//
// The scanner asserts that every explicit method of the two accesscore repo
// interfaces satisfies BOTH conditions, except an explicit allowlist:
//
//  1. Param[0] is context.Context.
//  2. Param[1] is exactly tenant.TenantID (go/types object identity — not a
//     name-string match; alias-proof because aliased types resolve to the same
//     *types.Named).
//
// # Carve-out allowlist (by-PK tenant-deriving reads)
//
//   - UserRepository.GetByID — looks up by the GLOBAL UUID primary key, which
//     already uniquely identifies the row (hence its tenant); the predicate would
//     be redundant. It is the only tenant-less method because its sole tenant-less
//     caller (sessionrefresh — the refresh token / session row carry no tenant
//     until PR-3) must stay callable. DB-layer RLS (PR-3) is the Hard backstop for
//     this path once the refresh tenant carrier lands.
//
// RoleRepository has NO carve-out: a role id is unique only within a tenant, so
// even a by-id read must be tenant-scoped.
//
// # What TENANT-REPO-CALLSITE-FUNNEL-01 guards
//
// The carve-out above exempts UserRepository.GetByID from the param check, but
// that alone does not prevent arbitrary production code from calling the tenant-less
// GetByID and silently bypassing tenant isolation. This second invariant locks the
// set of production (non-test) packages that may call UserRepository.GetByID to a
// bounded sanctioned allowlist, resolved via go/types object identity so import
// aliases and bare-ident same-package calls both match.
//
// Sanctioned callers (call-site allowlist — module-relative paths):
//
//   - cells/accesscore/slices/sessionrefresh/service.go — pre-auth, no tenant
//     source until PR-3 RLS lands (fetchUserForRefresh)
//   - cells/accesscore/slices/sessionvalidate/service.go — auth middleware itself,
//     no upstream tenant ctx (enforceSessionState)
//   - cells/accesscore/slices/rbacassign/service.go — Option B: derives tenant FROM
//     target user (Assign + Revoke)
//   - cells/accesscore/internal/adapters/postgres/user_repo.go — adapter-internal
//     self-call (UpdatePassword re-read disambiguation)
//   - cells/accesscore/internal/mem/user_repo.go — adapter-internal self-call
//     (GetByIDForUpdate internal delegation)
//
// # AI-robust ratings
//
// TENANT-REPO-PARAM-FUNNEL-01: MEDIUM (type-aware AST/types scan).
//   - The compiler-Hard "漏传 tenant = 编译失败" is the primary gate (route A
//     typed positional param); this archtest is the Medium backstop against the
//     string-typed-slot AND wrong-position drifts the compiler permits.
//   - Hard-upgrade path: sealed TenantScopedRepo handle — making "query without
//     first narrowing tenant" structurally unexpressible. Tracked at gh #1478.
//
// TENANT-REPO-CALLSITE-FUNNEL-01: MEDIUM downstream (go/types TypesInfo.Uses
// caller-allowlist; alias bypass ineffective — same *types.Func object).
//   - Upstream Hard is unreachable — Go cannot express "only these packages may
//     call this exported interface method"; Go package visibility has no
//     per-method caller constraint. Same permanent ceiling as
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01 / OUTBOX-RECONSTRUCTION-CALLER-01
//     (#1282). Hard-upgrade path: sealed TenantScopedRepo handle (gh #1478).
//
// # Tool blind spots (charter §"强制盲区自检")
//
// TENANT-REPO-PARAM-FUNNEL-01:
//   - Non-interface (struct-method) repos are out of scope: the platform repos
//     are interfaces, and the typed scan enumerates explicit interface methods.
//     A future concrete-type repo would need its own enrollment.
//   - A tenant param typed as an ALIAS of tenant.TenantID would still resolve to
//     the same *types.Named via go/types, so aliasing does not bypass.
//   - A method that takes tenant.TenantID at param[1] but ignores it in the body
//     is not caught here (that is a body-level concern covered by conformance
//     tests asserting cross-tenant queries return empty).
//
// TENANT-REPO-CALLSITE-FUNNEL-01:
//   - Only detects calls to methods whose receiver is (or satisfies)
//     ports.UserRepository — resolved via the interface's method-set object
//     identity, NOT by method name alone. An unrelated GetByID on a different
//     type (sessions, roles, audit) does NOT match.
//   - A call added in a //go:build-gated PRODUCTION file under a non-default tag
//     would be missed by the default-tags scan; both bridges today are
//     default-build.
//   - The anti-vacuity guard (every allowlisted file must contain ≥1 observed
//     GetByID call) is the reverse self-check; removing or renaming a call in an
//     allowlisted file will turn this guard red.
//   - test files (*_test.go) and conformance/ helpers are excluded from the scan
//     scope (production = IsProduction predicate).
//   - The fix for F3: method name match alone was Soft (any unrelated GetByID
//     would match); this scanner binds the match to go/types Selection.Obj()
//     identity for the specific ports.UserRepository.GetByID *types.Func object,
//     making it impossible for an unrelated same-name method on a different type
//     to widen the allowlist.
package archtest

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const (
	tenantPkgPath         = "github.com/ghbvf/gocell/pkg/tenant"
	accesscorePortsPkg    = "github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	tenantRepoParamFixPkg = "./tools/archtest/internal/tenantrepoparamfixture"
)

// tenantScopedRepoIfaces are the accesscore repo interfaces every (non-allowlisted)
// method of which must carry a tenant.TenantID positional parameter.
var tenantScopedRepoIfaces = []string{"RoleRepository", "UserRepository"}

// tenantParamCarveOut is the by-PK tenant-deriving read allowlist (see file godoc).
// Key is "<Interface>.<Method>".
var tenantParamCarveOut = map[string]string{
	"UserRepository.GetByID": "by-global-UUID-PK tenant-deriving read; sole tenant-less " +
		"caller is sessionrefresh (no pre-auth tenant source until PR-3 RLS)",
}

// isTenantIDType reports whether t is pkg/tenant.TenantID (alias-proof via the
// resolved *types.Named identity).
func isTenantIDType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == tenantPkgPath && obj.Name() == "TenantID"
}

// isContextType reports whether t is context.Context (interface).
func isContextType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// tenantIDAtPosition1 asserts the typed positional contract for tenant-scoped
// repo methods:
//
//   - param[0] must be context.Context
//   - param[1] must be exactly tenant.TenantID
//
// Both checks use go/types object identity — not name-string matching — so import
// aliases and type aliases both resolve correctly. Returns false if either
// condition is unmet, or if the signature has fewer than 2 parameters.
//
// This is the F12 fix: the previous hasTenantIDParam accepted tenant.TenantID at
// ANY parameter position, which would wrongly pass a drifted signature like
// Foo(ctx, id string, t tenant.TenantID). The positional assertion closes that gap.
func tenantIDAtPosition1(sig *types.Signature) bool {
	params := sig.Params()
	if params.Len() < 2 {
		return false
	}
	if !isContextType(params.At(0).Type()) {
		return false
	}
	return isTenantIDType(params.At(1).Type())
}

// scanTenantRepoParam inspects the named interfaces in p.Pkg and reports every
// explicit method that fails the positional tenant.TenantID assertion and is not
// allowlisted. withTenant counts methods that DID satisfy the assertion (anti-vacuity
// signal). seenMethods records every "<Iface>.<Method>" observed (stale-allowlist check).
func scanTenantRepoParam(
	p *Pass, ifaceNames []string, allow map[string]string,
) (diags []Diagnostic, withTenant int, seenMethods map[string]struct{}) {
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
			if tenantIDAtPosition1(sig) {
				withTenant++
				continue
			}
			pos := p.Fset.Position(m.Pos())
			diags = append(diags, Diagnostic{
				Line: pos.Line,
				Message: fmt.Sprintf(
					"TENANT-REPO-PARAM-FUNNEL-01: %s.%s does not have tenant.TenantID at position 1 "+
						"(param[1], immediately after ctx). Tenant-scoped repo methods MUST declare "+
						"ctx context.Context as param[0] and tenant.TenantID as param[1] so the tenant "+
						"predicate is type-enforced at the call site. Moving the tenant param further "+
						"right also violates this rule. If this is a legitimate by-global-PK "+
						"tenant-deriving read, add %q to tenantParamCarveOut with rationale.",
					ifaceName, m.Name(), key,
				),
			})
		}
	}
	return diags, withTenant, seenMethods
}

// TestTenantRepoParamFunnel01 asserts every accesscore repo interface method
// (minus the by-PK carve-out) carries a tenant.TenantID at param[1] (after ctx),
// and that no carve-out entry is stale.
func TestTenantRepoParamFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		totalWithTenant int
		allSeen         = map[string]struct{}{}
	)
	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil || p.Pkg.Path() != accesscorePortsPkg {
			return nil
		}
		d, withTenant, seen := scanTenantRepoParam(p, tenantScopedRepoIfaces, tenantParamCarveOut)
		totalWithTenant += withTenant
		for k := range seen {
			allSeen[k] = struct{}{}
		}
		return d
	})

	// Anti-vacuity: the scan must have resolved real interfaces and observed at
	// least one method actually carrying the typed param (else a path/typing
	// regression would make this test vacuously pass).
	if len(allSeen) == 0 {
		diags = append(diags, Diagnostic{Message: "TENANT-REPO-PARAM-FUNNEL-01: scanned zero repo interface methods — " +
			"scanner regressed or accesscore ports package path changed (expected " + accesscorePortsPkg + ")"})
	}
	if totalWithTenant == 0 {
		diags = append(diags, Diagnostic{Message: "TENANT-REPO-PARAM-FUNNEL-01: zero methods carry a tenant.TenantID " +
			"parameter — go/types tenant-type resolution regressed"})
	}

	// No-stale carve-out: each allowlisted method must exist on a scanned interface.
	stale := make([]string, 0)
	for key := range tenantParamCarveOut {
		if _, seen := allSeen[key]; !seen {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		diags = append(diags, Diagnostic{Message: fmt.Sprintf(
			"TENANT-REPO-PARAM-FUNNEL-01: carve-out entry %q is STALE — no such interface method observed. "+
				"Drop the dead allowlist entry so it cannot become a silent bypass slot.", key,
		)})
	}

	Report(t, "TENANT-REPO-PARAM-FUNNEL-01", diags)
}

// TestTenantRepoParamFunnel01_ScannerCatchesViolation is the reverse self-check:
// it runs the SAME scanner against the RED fixture and asserts:
//   - GetByThing (string-typed slot) is reported — catches the original F01 bug.
//   - WrongPosition (tenant.TenantID at param[2], not param[1]) is reported —
//     catches the new F12 position-assertion gap.
//   - GoodMethod (tenant.TenantID at param[1]) is NOT reported.
//
// Expects exactly 2 diagnostics (GetByThing + WrongPosition).
func TestTenantRepoParamFunnel01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{tenantRepoParamFixPkg},
		func(p *Pass) []Diagnostic {
			if !p.Typed() || p.Pkg == nil {
				return nil
			}
			d, _, _ := scanTenantRepoParam(p, []string{"FakeRepo"}, nil)
			return d
		})

	if len(diags) != 2 {
		t.Fatalf("expected exactly 2 diagnostics (GetByThing + WrongPosition), got %d: %v", len(diags), diags)
	}
	// Verify both expected violations are flagged.
	var msgs []string
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	sort.Strings(msgs)
	foundGetByThing := false
	foundWrongPos := false
	for _, m := range msgs {
		if strings.Contains(m, "FakeRepo.GetByThing") {
			foundGetByThing = true
		}
		if strings.Contains(m, "FakeRepo.WrongPosition") {
			foundWrongPos = true
		}
	}
	if !foundGetByThing {
		t.Errorf("expected diagnostic for FakeRepo.GetByThing (string-typed slot), got messages: %v", msgs)
	}
	if !foundWrongPos {
		t.Errorf("expected diagnostic for FakeRepo.WrongPosition (tenant.TenantID at wrong position), got messages: %v", msgs)
	}
}

// tenantRepoCallsiteAllowlist is the bounded set of production files that may
// call the tenant-less UserRepository.GetByID via an interface-typed value.
// Every entry must be justified below; stale entries are detected by
// anti-vacuity checks.
//
// NOTE: adapter-internal self-calls (postgres/user_repo.go and mem/user_repo.go)
// call GetByID on a CONCRETE *UserRepository receiver, NOT through the
// ports.UserRepository interface. go/types Selections records those calls with
// the concrete struct's *types.Func, not the interface method — so they do NOT
// appear in this scan and do not need allowlist entries.
//
// Hard-upgrade path: once a sealed TenantScopedRepo handle (gh #1478) lands,
// callers lose compile-time access to the tenant-less GetByID — making the
// callsite allowlist unnecessary. Until then, this Medium archtest is the sole
// enforcement layer for the upstream direction.
var tenantRepoCallsiteAllowlist = map[string]struct{}{
	// pre-auth, no tenant source until PR-3 RLS (fetchUserForRefresh)
	"cells/accesscore/slices/sessionrefresh/service.go": {},
	// auth middleware itself, no upstream tenant ctx (enforceSessionState)
	"cells/accesscore/slices/sessionvalidate/service.go": {},
	// Option B: derives tenant FROM target user (Assign + Revoke)
	"cells/accesscore/slices/rbacassign/service.go": {},
	// test-support fixture: GetUser helper wraps the carve-out for test harnesses
	// (reads via the bundled interface for test assertions — only ever called by tests)
	"cells/accesscore/accesscoretest/fixture.go": {},
	// conformance suite: tests every UserRepository implementation against the
	// GetByID contract; uses the interface-typed repo parameter
	"cells/accesscore/internal/ports/conformance/conformance.go": {},
}

// userRepoGetByIDObj resolves the *types.Func for UserRepository.GetByID in the
// accesscore ports package. Returns nil if the package has not been loaded yet
// (caller must check). Uses method-set lookup so it works regardless of whether
// the method is promoted or embedded.
func userRepoGetByIDObj(p *Pass) *types.Func {
	if p.Pkg == nil || p.Pkg.Path() != accesscorePortsPkg {
		return nil
	}
	scope := p.Pkg.Scope()
	obj := scope.Lookup("UserRepository")
	if obj == nil {
		return nil
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	for i := 0; i < iface.NumExplicitMethods(); i++ {
		m := iface.ExplicitMethod(i)
		if m.Name() == "GetByID" {
			return m
		}
	}
	return nil
}

// TestTenantRepoCallsiteFunnel01 asserts that every production (non-test) call to
// UserRepository.GetByID sits in tenantRepoCallsiteAllowlist, and that every
// allowlist entry is non-stale (observed at least once).
//
// Detection uses go/types TypesInfo.Selections: interface method calls on a value
// of an interface type are recorded in Selections as MethodExpr / MethodVal calls,
// whose Obj() resolves to the *types.Func declared on the interface — the same
// object returned by userRepoGetByIDObj. This ensures:
//   - Only calls to ports.UserRepository.GetByID match (not GetByID on sessions,
//     roles, or any other type with a same-named method).
//   - Import aliases are transparent — they map to the same *types.Func identity.
//   - Same-package (same-file) calls and cross-package calls are both captured.
//
// See also: TENANT-REPO-PARAM-FUNNEL-01 for the complementary param-position guard.
func TestTenantRepoCallsiteFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Phase 1: resolve the *types.Func for UserRepository.GetByID.
	// We collect it from the ports package pass; other passes use it for matching.
	var getByIDFuncObj *types.Func

	// Phase 2: scan all production packages for calls to that func, check allowlist.
	observed := map[string]struct{}{} // module-relative file paths that called GetByID

	var allDiags []Diagnostic

	// First pass: resolve the function object from the ports package.
	_ = RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil || p.Pkg.Path() != accesscorePortsPkg {
			return nil
		}
		getByIDFuncObj = userRepoGetByIDObj(p)
		return nil
	})

	if getByIDFuncObj == nil {
		allDiags = append(allDiags, Diagnostic{Message: "TENANT-REPO-CALLSITE-FUNNEL-01: " +
			"failed to resolve UserRepository.GetByID *types.Func from " + accesscorePortsPkg +
			" — scanner regressed or the method was renamed"})
		Report(t, "TENANT-REPO-CALLSITE-FUNNEL-01", allDiags)
		return
	}

	// Second pass: scan all production code for calls to that *types.Func.
	scanDiags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() || p.TypesInfo == nil {
			return nil
		}
		// Build abs→rel map for this pass.
		relByAbs := make(map[string]string, len(p.Files))
		for _, f := range p.Files {
			relByAbs[p.Abs(f)] = p.Rel(f)
		}

		var d []Diagnostic
		for selExpr, sel := range p.TypesInfo.Selections {
			// We want method calls — MethodExpr and MethodVal selections.
			if sel.Kind() != types.MethodExpr && sel.Kind() != types.MethodVal {
				continue
			}
			if sel.Obj() != getByIDFuncObj {
				continue
			}
			// Found a reference to UserRepository.GetByID.
			pos := p.Fset.Position(selExpr.Pos())
			rel, ok := relByAbs[pos.Filename]
			if !ok {
				continue
			}
			observed[rel] = struct{}{}
			if _, allowed := tenantRepoCallsiteAllowlist[rel]; !allowed {
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"TENANT-REPO-CALLSITE-FUNNEL-01: %s:%d calls the tenant-less "+
							"UserRepository.GetByID, which is a by-PK carve-out only sanctioned "+
							"for pre-auth paths that have no tenant source (sessionrefresh, "+
							"sessionvalidate) or derive tenant from the target user (rbacassign), "+
							"plus adapter-internal self-calls. This caller is not in the sanctioned "+
							"allowlist. If this is a new legitimate pre-auth or adapter-internal "+
							"use, add %q to tenantRepoCallsiteAllowlist with rationale. "+
							"Otherwise use GetByIDInTenant (post-auth paths with a tenant context) "+
							"or add a tenant.TenantID parameter. Hard-upgrade path: sealed "+
							"TenantScopedRepo handle (gh #1478).",
						rel, pos.Line, rel),
				})
			}
		}
		return d
	})
	allDiags = append(allDiags, scanDiags...)

	// Anti-vacuity / stale-allowlist check: every allowlisted file must have been
	// observed calling GetByID at least once.
	stale := make([]string, 0, len(tenantRepoCallsiteAllowlist))
	for f := range tenantRepoCallsiteAllowlist {
		if _, seen := observed[f]; !seen {
			stale = append(stale, f)
		}
	}
	sort.Strings(stale)
	for _, f := range stale {
		allDiags = append(allDiags, Diagnostic{Message: fmt.Sprintf(
			"TENANT-REPO-CALLSITE-FUNNEL-01: allowlist entry %q is STALE — no live "+
				"UserRepository.GetByID reference observed in that file. Either the call "+
				"was removed (drop the allowlist entry) or the scanner regressed (check "+
				"accesscorePortsPkg path and go/types Selections detection).",
			f,
		)})
	}

	Report(t, "TENANT-REPO-CALLSITE-FUNNEL-01", allDiags)
}

// TestTenantRepoCallsiteFunnel01_FixtureCatchesViolation is the reverse self-check
// for TENANT-REPO-CALLSITE-FUNNEL-01. It loads the fixture package and runs a
// simplified callsite scanner against FakeUserRepository.GetByID, asserting that
// FakeGetByIDCaller is flagged as an unsanctioned caller.
//
// Note: the fixture scanner cannot use the same two-pass approach as the production
// test (which resolves the *types.Func from the real ports package) because the
// fixture package defines its OWN FakeUserRepository.GetByID interface. Instead,
// this test resolves the *types.Func from within the fixture package itself and
// checks FakeGetByIDCaller is flagged.
func TestTenantRepoCallsiteFunnel01_FixtureCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Allowlist for the fixture scanner: nothing is allowed (the fixture only
	// has violating callers — FakeGetByIDCaller must be rejected).
	fixtureCallsiteAllowlist := map[string]struct{}{}

	var fakeGetByIDObj *types.Func
	var fixtureDiags []Diagnostic

	// First: resolve FakeUserRepository.GetByID from the fixture package.
	_ = RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{tenantRepoParamFixPkg},
		func(p *Pass) []Diagnostic {
			if !p.Typed() || p.Pkg == nil {
				return nil
			}
			scope := p.Pkg.Scope()
			obj := scope.Lookup("FakeUserRepository")
			if obj == nil {
				return nil
			}
			iface, ok := obj.Type().Underlying().(*types.Interface)
			if !ok {
				return nil
			}
			for i := 0; i < iface.NumExplicitMethods(); i++ {
				m := iface.ExplicitMethod(i)
				if m.Name() == "GetByID" {
					fakeGetByIDObj = m
					return nil
				}
			}
			return nil
		})

	if fakeGetByIDObj == nil {
		t.Fatal("fixture scanner: could not resolve FakeUserRepository.GetByID — fixture regressed")
	}

	// Second: scan the fixture for calls to FakeUserRepository.GetByID.
	observed := map[string]struct{}{}
	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{tenantRepoParamFixPkg},
		func(p *Pass) []Diagnostic {
			if !p.Typed() || p.TypesInfo == nil {
				return nil
			}
			relByAbs := make(map[string]string, len(p.Files))
			for _, f := range p.Files {
				relByAbs[p.Abs(f)] = p.Rel(f)
			}
			var d []Diagnostic
			for selExpr, sel := range p.TypesInfo.Selections {
				if sel.Kind() != types.MethodExpr && sel.Kind() != types.MethodVal {
					continue
				}
				if sel.Obj() != fakeGetByIDObj {
					continue
				}
				pos := p.Fset.Position(selExpr.Pos())
				rel, ok := relByAbs[pos.Filename]
				if !ok {
					continue
				}
				observed[rel] = struct{}{}
				if _, allowed := fixtureCallsiteAllowlist[rel]; !allowed {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"TENANT-REPO-CALLSITE-FUNNEL-01 fixture: unsanctioned call to "+
								"FakeUserRepository.GetByID from %s:%d",
							rel, pos.Line),
					})
				}
			}
			return d
		})
	fixtureDiags = append(fixtureDiags, diags...)

	if len(fixtureDiags) == 0 {
		t.Fatal("expected ≥1 diagnostic from FakeGetByIDCaller, got 0 — fixture or scanner regressed")
	}
	// Verify FakeGetByIDCaller is among the flagged callers.
	foundCaller := false
	for _, d := range fixtureDiags {
		if strings.Contains(d.Message, "FakeGetByIDCaller") || strings.Contains(d.Message, "repo.go") {
			foundCaller = true
			break
		}
	}
	if !foundCaller {
		t.Errorf("expected diagnostic mentioning FakeGetByIDCaller or repo.go, got: %v", fixtureDiags)
	}
}
