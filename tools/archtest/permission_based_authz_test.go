//go:build archtest

// INVARIANT: PERMISSION-BASED-AUTHZ-01
//
// PERMISSION-BASED-AUTHZ-01 forbids role-literal authorization gates
// (auth.AnyRole / auth.SelfOr / auth.RequireAnyRole) in business handlers
// that have NOT been migrated to ABAC permission checks.
//
// A "role-literal gate" is a call whose receiver resolves (via go/types) to a
// local alias of github.com/ghbvf/gocell/runtime/auth and whose selector is
// AnyRole, SelfOr, or RequireAnyRole. These gates hard-code a role identity at
// the call site, bypassing the ABAC PDP (auth.RequirePermission +
// pkg/authz.Permission).
//
// # AI-robust grade: Medium
//
// Downstream Medium: type-aware CallExpr scan via ResolvePackageRef detects
// every call regardless of import alias; CI fails loud on any new role-literal
// gate in a non-allowlisted file. The rule is NOT Hard because auth.AnyRole
// must remain callable by the allowlisted cells until PR-10b: the constraint
// cannot be expressed as a compile error while the migration is in progress.
//
// Upstream funnel: the allowlist is a migration ledger (see below). PR-10b
// shrinks it to empty, at which point the rule becomes a zero-allowlist Hard
// target (codegen-derived gate + golden per Hard 范本 "codegen funnel +
// golden"). Hard-ification end state: permission declared in contract.yaml →
// cellgen-derived gate + golden (tracked for PR-10b / PR-13).
//
// Funnel 双向锁评级 (ai-robust.md §"Funnel 双向锁评级"):
//
//	下游 Medium — archtest typed callsite scan, fail-loud in CI;
//	  alias-safe (ResolvePackageRef resolves the import path, not the local name).
//	上游 Medium — allowlist convergence: PR-10b migrates remaining cells to
//	  auth.RequirePermission(pkg/authz.Permission) and deletes every entry here.
//	Hard-ification tracker: permission-gate codegen funnel → gh #PR-13.
//
// # Allowlist
//
// The files below legitimately still use role-literal gates as of PR-10a and
// are allowlisted until the corresponding PR-10b migration. auditquery is NOT
// allowlisted — it was migrated to auth.RequirePermission(authz.PermAuditRead)
// in PR-10a (#1348) and must not regress.
//
// To migrate a file: replace every auth.AnyRole/SelfOr/RequireAnyRole call
// with auth.RequirePermission(authz.Perm*), declare the permission in
// contract.yaml, remove the entry below, and confirm this test stays GREEN.
//
// # Blind spots
//
//   - Method value capture (`p := auth.AnyRole; _ = p`): not flagged because
//     the assigned local is not itself a call. Production code does not use this
//     form; a future PR can extend the scan to SelectorExpr if needed.
//   - Dot-import (`import . ".../runtime/auth"`): ResolvePackageRef resolves
//     dot-imported bare identifiers via go/types info — this IS covered.
//   - Import alias (`import ra ".../runtime/auth"; ra.AnyRole(...)`):
//     ResolvePackageRef normalises the import path — this IS covered.
//
// Reverse self-check: TestPermissionBasedAuthz_ReverseFixture asserts the scan
// fires on testdata/permission_based_authz_red, proving the rule is not vacuous.
package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const rulePermissionBasedAuthz01 = "PERMISSION-BASED-AUTHZ-01"

// permissionBasedAuthzRoleGateSelectors is the set of auth.* selector names
// that constitute a role-literal authorization gate. All three wrap role-string
// arguments rather than ABAC permission identifiers.
var permissionBasedAuthzRoleGateSelectors = map[string]struct{}{
	"AnyRole":        {},
	"SelfOr":         {},
	"RequireAnyRole": {},
}

// permissionBasedAuthzAllowlist is the migration ledger: handler files that
// still legitimately call role-literal gates pending PR-10b migration.
// Paths are module-relative slash paths (p.Rel values from the corecells module).
// auditquery is deliberately absent — it was migrated in PR-10a.
var permissionBasedAuthzAllowlist = map[string]struct{}{
	"corecells/configcore/slices/configread/handler.go":    {},
	"corecells/configcore/slices/configwrite/handler.go":   {},
	"corecells/configcore/slices/configpublish/handler.go": {},
	"corecells/configcore/slices/flagwrite/handler.go":     {},
	"corecells/configcore/slices/featureflag/handler.go":   {},
	"corecells/accesscore/slices/policymanage/handler.go":  {},
	"corecells/accesscore/slices/identitymanage/handler.go": {},
	"corecells/accesscore/slices/rbaccheck/handler.go":     {},
}

// scanPermissionBasedAuthzViolations scans a single file for role-literal
// authorization gate calls whose file is not in the allowlist.
// It uses ResolvePackageRef to resolve the callee import path, so import
// aliases and dot-imports are handled uniformly.
func scanPermissionBasedAuthzViolations(p *Pass, f *ast.File, rel string, allowlist map[string]struct{}) []Diagnostic {
	if _, allowed := allowlist[rel]; allowed {
		return nil
	}
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != authRuntimeImportPath {
			return
		}
		if _, banned := permissionBasedAuthzRoleGateSelectors[name]; !banned {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(call.Pos()).Line,
			Message: "auth." + name + " role-literal gate in business handler violates " +
				rulePermissionBasedAuthz01 +
				"; migrate to auth.RequirePermission(authz.Perm*) and remove from allowlist",
		})
	})
	return out
}

// TestPermissionBasedAuthzAllowlist_Ceiling guards that the migration allowlist
// does not grow past its known maximum (8 entries as of PR-10a). An accidental
// NEW entry that suppresses a real PERMISSION-BASED-AUTHZ-01 violation would
// silently increase the ceiling — this test makes that visible in CI.
//
// PR-10b intent: each PR-10b commit removes one or more entries and decrements
// this ceiling toward 0. When the allowlist is empty this test becomes trivially
// true and can be removed together with the allowlist.
func TestPermissionBasedAuthzAllowlist_Ceiling(t *testing.T) {
	const maxAllowlistSize = 8 // PR-10a known maximum; PR-10b decrements toward 0
	if got := len(permissionBasedAuthzAllowlist); got > maxAllowlistSize {
		t.Errorf("permissionBasedAuthzAllowlist has %d entries, want ≤ %d — "+
			"new entries must not be added (migrate to auth.RequirePermission instead); "+
			"PR-10b shrinks this toward 0", got, maxAllowlistSize)
	}
}

// TestPermissionBasedAuthz_01 enforces PERMISSION-BASED-AUTHZ-01 over
// corecells business handlers. Any auth.AnyRole / auth.SelfOr /
// auth.RequireAnyRole call outside the migration allowlist is a violation.
//
// The scan is import-aware: ResolvePackageRef resolves the callee to its
// canonical go/types import path (github.com/ghbvf/gocell/runtime/auth),
// so import aliases and dot-imports are all caught.
func TestPermissionBasedAuthz_01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Typed(TypedOpts{}, []string{"./corecells/..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				out = append(out, scanPermissionBasedAuthzViolations(p, f, rel, permissionBasedAuthzAllowlist)...)
			}
			return out
		})

	Report(t, rulePermissionBasedAuthz01, diags)
}

// TestPermissionBasedAuthz_ReverseFixture loads the synthetic RED fixture and
// asserts the scan fires — guards against the rule logic silently regressing to
// a vacuous pass. The fixture (testdata/permission_based_authz_red) is a
// standalone module with a handler that calls auth.AnyRole; it is NOT in the
// allowlist, so the scan must emit ≥ 1 diagnostic.
//
// Anti-vacuity: the production scan (TestPermissionBasedAuthz_01) is
// vacuously green for allowlisted files — this test proves the scanner can
// actually detect a violation when pointed at an un-allowlisted handler.
func TestPermissionBasedAuthz_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "permission_based_authz_red")

	emptyAllowlist := map[string]struct{}{}

	var diags []Diagnostic
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanPermissionBasedAuthzViolations(p, f, rel, emptyAllowlist)...)
			}
			return nil
		})

	assert.GreaterOrEqual(t, len(diags), 1,
		"reverse fixture: expected ≥1 diagnostic for the role-literal auth.AnyRole callsite "+
			"in testdata/permission_based_authz_red — "+
			rulePermissionBasedAuthz01+" must fire, else the rule is vacuous")
}
