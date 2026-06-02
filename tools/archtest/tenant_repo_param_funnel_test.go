// tenant_repo_param_funnel_test.go — guards that every tenant-scoped accesscore
// repo interface method carries a tenant.TenantID positional parameter.
//
//   - INVARIANT: TENANT-REPO-PARAM-FUNNEL-01
//
// # What this guards (#1337 PR-2, Model A)
//
// Tenant isolation in GoCell is enforced as a mandatory typed positional
// parameter on the platform repo interfaces (route A — the only shape that can
// reach a type-system Hard guarantee). The DOMINANT guarantee is the compiler:
// once RoleRepository.GetByUserID(ctx, t tenant.TenantID, …) exists, a caller
// that "forgets the tenant" cannot compile — no fixture needed for that vector.
//
// This archtest guards the COMPLEMENTARY drift vector the compiler cannot catch:
// a NEW (or edited) repo method whose tenant slot is declared as a plain `string`
// instead of `tenant.TenantID`. Such a method type-checks fine but reopens the
// "no tenant predicate / wrong-typed tenant" hole. The scanner asserts that every
// explicit method of the two accesscore repo interfaces carries at least one
// parameter of type pkg/tenant.TenantID, except an explicit allowlist.
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
// # AI-robust rating (charter §"立项硬门槛" / T2.5 [R1 F-A8])
//
// MEDIUM (type-aware AST/types scan). The compiler-Hard "漏传 tenant = 编译失败"
// is the primary gate (route A typed positional param); this archtest is the
// Medium backstop against the string-typed-slot drift the compiler permits.
//
// Hard-upgrade path (charter requires a tracked issue for a Medium funnel): the
// only strictly-Hard shape is a sealed TenantScopedRepo handle obtained via
// repo.Scoped(t) on which the query methods hang — making "query without first
// narrowing tenant" structurally unexpressible. The EPIC ([R1 F-A8]) chose the
// positional-param form deliberately (less API churn); the sealed-handle upgrade
// is tracked at gh #1478.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Non-interface (struct-method) repos are out of scope: the platform repos
//     are interfaces, and the typed scan enumerates explicit interface methods.
//     A future concrete-type repo would need its own enrollment.
//   - A tenant param typed as an ALIAS of tenant.TenantID would still resolve to
//     the same *types.Named via go/types, so aliasing does not bypass.
//   - A method that takes tenant.TenantID but ignores it in the body is not
//     caught here (that is a body-level concern covered by conformance tests
//     asserting cross-tenant queries return empty), not a signature concern.
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

// hasTenantIDParam reports whether sig has at least one parameter of type
// pkg/tenant.TenantID (alias-proof via the resolved *types.Named identity).
func hasTenantIDParam(sig *types.Signature) bool {
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if named, ok := params.At(i).Type().(*types.Named); ok {
			obj := named.Obj()
			if obj.Pkg() != nil && obj.Pkg().Path() == tenantPkgPath && obj.Name() == "TenantID" {
				return true
			}
		}
	}
	return false
}

// scanTenantRepoParam inspects the named interfaces in p.Pkg and reports every
// explicit method that lacks a tenant.TenantID parameter and is not allowlisted.
// withTenant counts methods that DID carry the param (anti-vacuity signal).
// seenMethods records every "<Iface>.<Method>" observed (stale-allowlist check).
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
			if hasTenantIDParam(sig) {
				withTenant++
				continue
			}
			pos := p.Fset.Position(m.Pos())
			diags = append(diags, Diagnostic{
				Line: pos.Line,
				Message: fmt.Sprintf(
					"TENANT-REPO-PARAM-FUNNEL-01: %s.%s has no tenant.TenantID positional parameter. "+
						"Tenant-scoped repo methods MUST take tenant.TenantID (not a plain string) so the "+
						"tenant predicate is type-enforced. If this is a legitimate by-global-PK tenant-deriving "+
						"read, add %q to tenantParamCarveOut with rationale.",
					ifaceName, m.Name(), key,
				),
			})
		}
	}
	return diags, withTenant, seenMethods
}

// TestTenantRepoParamFunnel01 asserts every accesscore repo interface method
// (minus the by-PK carve-out) carries a tenant.TenantID positional parameter,
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
// it runs the SAME scanner against the RED fixture and asserts the string-typed
// tenant slot is reported while the properly-typed method is not. Proves the
// scanner is not vacuous.
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

	if len(diags) != 1 {
		t.Fatalf("expected exactly 1 diagnostic (FakeRepo.GetByThing), got %d: %v", len(diags), diags)
	}
	if msg := diags[0].Message; !strings.Contains(msg, "FakeRepo.GetByThing") {
		t.Fatalf("expected diagnostic to name FakeRepo.GetByThing, got: %s", msg)
	}
}
