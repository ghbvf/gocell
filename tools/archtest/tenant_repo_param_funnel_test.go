//go:build archtest

// tenant_repo_param_funnel_test.go — guards that every tenant-scoped platform
// repo interface method carries a tenant.TenantID positional parameter.
// Enrolled repos: accesscore (RoleRepository, UserRepository, PolicyRepository)
// and configcore (ConfigRepository, FlagRepository).
//
//   - INVARIANT: TENANT-REPO-PARAM-FUNNEL-01
//
// # What TENANT-REPO-PARAM-FUNNEL-01 guards (#1337 PR-2 / PR-2b, Model A)
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
// The scanner asserts that every explicit method of the enrolled repo interfaces
// satisfies BOTH conditions, except an explicit allowlist:
//
//  1. Param[0] is context.Context.
//  2. Param[1] is exactly tenant.TenantID (go/types object identity — not a
//     name-string match; alias-proof because aliased types resolve to the same
//     *types.Named).
//
// # Enrolled interfaces
//
// accesscore ports (accesscorePortsPkg):
//   - RoleRepository — no carve-out.
//   - UserRepository — no carve-out. PR-3b (#1617) deleted the tenant-less
//     by-PK GetByID; every UserRepository method now carries tenant.TenantID.
//   - PolicyRepository — carve-out: RepoReady (schema-existence probe, not a
//     tenant-scoped data read). PR-6 (#1344) ABAC policy model; a policy id is
//     unique only within a tenant, so every DATA method is tenant-scoped — only
//     the readiness probe is tenant-less.
//
// configcore ports (configcorePortsPkg):
//   - ConfigRepository — carve-out: RepoReady (schema-existence probe, not a
//     tenant-scoped data read).
//   - FlagRepository — no carve-out.
//
// # Carve-out allowlist (schema probes)
//
//   - ConfigRepository.RepoReady — schema-existence probe (exercises
//     config_entries + feature_flags table presence); not a tenant-scoped data
//     read, so a tenant predicate is meaningless.
//   - PolicyRepository.RepoReady — schema-existence probe (policies table
//     reachability, #1346 PR-8); not a tenant-scoped data read, so a tenant
//     predicate is meaningless.
//
// RoleRepository has NO carve-out: a role id is unique only within a tenant, so
// even a by-id read must be tenant-scoped.
// FlagRepository has NO carve-out: flags are keyed by (tenant, key).
//
// # Retired: TENANT-REPO-CALLSITE-FUNNEL-01 (PR-3b #1617)
//
// That invariant locked the bounded set of production callers of the tenant-less
// UserRepository.GetByID by-PK carve-out. PR-3b deleted that method — the auth path
// now derives the tenant (login from the request, refresh/validate from the session
// row via the sessions.tenant_id carrier, rbacassign from the internal contract) and
// uses GetByIDInTenant under a tenant scope — so there is no tenant-less method left
// to fence. The upstream direction is now compiler-Hard.
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
package archtest

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const (
	// Derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01) so a
	// module rename updates exactly one place — never a bare literal.
	tenantPkgPath = PlatformFrameworkModulePath + "/pkg/tenant"
	// accesscorePortsPkg is also reused by rowscope_repo_param_funnel_test.go
	// (ROWSCOPE-REPO-PARAM-FUNNEL-01, #1709) — update both files if this path changes.
	accesscorePortsPkg = PlatformCellsModulePath + "/accesscore/internal/ports"
	configcorePortsPkg = PlatformCellsModulePath + "/configcore/internal/ports"
	// tenantRepoParamFixPkg is a relative load path for go/packages — NOT a
	// platform import path — so it is intentionally not derived from PlatformModulePath.
	tenantRepoParamFixPkg = "./tools/archtest/internal/tenantrepoparamfixture"
)

// tenantScopedRepoIfaces are the platform repo interfaces every (non-allowlisted)
// method of which must carry a tenant.TenantID positional parameter. Each name
// is looked up via Scope.Lookup in each enrolled ports package; if an interface
// name is not present in a package the Lookup returns nil and the scanner
// silently skips it — so the same list works correctly across both packages.
var tenantScopedRepoIfaces = []string{
	// accesscore ports (accesscorePortsPkg)
	"RoleRepository",
	"UserRepository",
	"PolicyRepository",
	// configcore ports (configcorePortsPkg)
	"ConfigRepository",
	"FlagRepository",
}

// tenantParamCarveOut is the by-PK tenant-deriving read and schema-probe
// allowlist (see file godoc). Key is "<Interface>.<Method>".
var tenantParamCarveOut = map[string]string{
	// UserRepository.GetByID (the by-PK tenant-deriving carve-out) was DELETED in
	// PR-3b (#1617): every UserRepository method now carries a tenant.TenantID — the
	// auth path derives the tenant (login from request, refresh/validate from the
	// session row, rbacassign from the contract) and uses GetByIDInTenant under
	// scope. TENANT-REPO-CALLSITE-FUNNEL-01 was retired together with it.
	"ConfigRepository.RepoReady": "healthz schema-existence probe (exercises config_entries + " +
		"feature_flags table presence); not a tenant-scoped data read, so a tenant predicate is meaningless",
	"PolicyRepository.RepoReady": "healthz schema-existence probe (policies table reachability, " +
		"#1346 PR-8); not a tenant-scoped data read, so a tenant predicate is meaningless",
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

// enrolledPortsPkgs is the closed set of ports packages whose repo interfaces
// are enrolled in TENANT-REPO-PARAM-FUNNEL-01. The scanner runs for any Pass
// whose package path is in this set; other packages are skipped. Each enrolled
// package declares a subset of tenantScopedRepoIfaces — Scope.Lookup silently
// returns nil for interfaces not declared in a given package, so the same
// interface list works across all enrolled packages without filtering.
var enrolledPortsPkgs = map[string]struct{}{
	accesscorePortsPkg: {},
	configcorePortsPkg: {},
}

// TestTenantRepoParamFunnel01 asserts every enrolled platform repo interface
// method (minus the carve-out allowlist) carries a tenant.TenantID at param[1]
// (after ctx), and that no carve-out entry is stale.
//
// Enrolled ports packages: accesscore (RoleRepository, UserRepository,
// PolicyRepository) and configcore (ConfigRepository, FlagRepository).
func TestTenantRepoParamFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		totalWithTenant int
		allSeen         = map[string]struct{}{}
	)
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		if _, enrolled := enrolledPortsPkgs[p.Pkg.Path()]; !enrolled {
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
			"scanner regressed or enrolled ports package paths changed (expected " +
			accesscorePortsPkg + " and " + configcorePortsPkg + ")"})
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

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{tenantRepoParamFixPkg}), func(p *Pass) []Diagnostic {
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

// TENANT-REPO-CALLSITE-FUNNEL-01 retired in PR-3b (#1617): it locked the bounded
// set of production callers of the tenant-less UserRepository.GetByID. That
// by-PK carve-out method was deleted (every UserRepository method now carries a
// tenant.TenantID), so there is nothing left to fence and no callsite allowlist
// to maintain. The upstream direction is now compiler-Hard (no tenant-less method
// to call); the param-position guard above (TENANT-REPO-PARAM-FUNNEL-01) remains.
