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
//	上游 Medium — allowlist convergence: each PR-10x migrates remaining cells to
//	  auth.RequirePermission(pkg/authz.Permission) and deletes its entries here
//	  (PR-10a auditquery, PR-10b configcore done; PR-10c accesscore pending).
//	Hard-ification tracker: permission-gate codegen funnel → gh #PR-13.
//
// # Allowlist
//
// The 3 files below legitimately still use role-literal gates pending PR-10c
// (accesscore — they carry SelfOr ownership gates needing custom policy funcs).
// auditquery (PR-10a) and the 5 configcore slices (PR-10b, #1348) are NOT
// allowlisted — they were migrated to auth.RequirePermission(authz.Perm*) and
// must not regress.
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
// The allowlist itself is doubly guarded: TestPermissionBasedAuthzAllowlist_Ceiling
// freezes it to an exact set (no silent grow/swap — cheap, runs PR-time), and
// TestPermissionBasedAuthz_01 flags any allowlist entry with no live gate as STALE
// (no dead bypass slot — typed scan, nightly).
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
// still legitimately call role-literal gates pending their PR-10x migration.
// Paths are module-relative slash paths (p.Rel values from the corecells module).
// auditquery (PR-10a) and the 5 configcore slices (PR-10b) are deliberately
// absent — they were migrated to auth.RequirePermission and must not regress.
// Only the 3 accesscore slices remain, pending PR-10c (they carry SelfOr
// ownership gates needing custom policy funcs).
var permissionBasedAuthzAllowlist = map[string]struct{}{
	"corecells/accesscore/slices/policymanage/handler.go":   {},
	"corecells/accesscore/slices/identitymanage/handler.go": {},
	"corecells/accesscore/slices/rbaccheck/handler.go":      {},
}

// expectedPermissionBasedAuthzAllowlist is the FROZEN exact-set expectation for
// permissionBasedAuthzAllowlist. TestPermissionBasedAuthzAllowlist_Ceiling asserts
// the live allowlist equals this set, so a silent SWAP (replace one accesscore path
// with a new exception) or GROW fails CI even though len is unchanged — a bare len
// ceiling cannot catch a swap. Each PR-10x drain (PR-10c removes the 3 accesscore
// entries) MUST update BOTH maps in the same PR; that forced, explicit edit is the
// guard (golden-like freeze), not redundancy.
var expectedPermissionBasedAuthzAllowlist = map[string]struct{}{
	"corecells/accesscore/slices/policymanage/handler.go":   {},
	"corecells/accesscore/slices/identitymanage/handler.go": {},
	"corecells/accesscore/slices/rbaccheck/handler.go":      {},
}

// scanPermissionBasedAuthzViolations scans a single file for role-literal
// authorization gate calls. A non-allowlisted file with a banned gate yields a
// violation. An allowlisted file with a banned gate yields NO violation but is
// recorded in observed (when non-nil) so the caller can prove the allowlist entry
// is still live (no-stale anti-vacuity) — an entry whose gate was already migrated
// away is then flagged after the full scan.
// It uses ResolvePackageRef to resolve the callee import path, so import aliases
// and dot-imports are handled uniformly.
func scanPermissionBasedAuthzViolations(p *Pass, f *ast.File, rel string, allowlist, observed map[string]struct{}) []Diagnostic {
	_, allowed := allowlist[rel]
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != authRuntimeImportPath {
			return
		}
		if _, banned := permissionBasedAuthzRoleGateSelectors[name]; !banned {
			return
		}
		if allowed {
			// Allowlisted: this entry is still live (carries a real role-literal
			// gate). Record it for the post-scan no-stale check; emit no violation.
			if observed != nil {
				observed[rel] = struct{}{}
			}
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

// TestPermissionBasedAuthzAllowlist_Ceiling is the EXACT-SET FREEZE on the
// migration allowlist: it asserts permissionBasedAuthzAllowlist equals the frozen
// expectedPermissionBasedAuthzAllowlist. A bare len ceiling could not catch a SWAP
// (replacing one accesscore path with a new configcore exception keeps len=3 and
// silently suppresses a real PERMISSION-BASED-AUTHZ-01 violation); the exact-set
// assertion makes any add / remove / swap fail loudly.
//
// This test is CHEAP (in-memory map compare, no packages.Load) and therefore runs
// at PR-time via hack/verify-archtest-invariants.sh — allowlist tampering fails at
// PR-merge, not only nightly. The live no-stale anti-vacuity (each allowlisted file
// still carries a real gate) rides the heavier typed scan in
// TestPermissionBasedAuthz_01 (nightly bucket).
//
// Migration intent: each PR-10x drain (PR-10c removes the 3 accesscore slices)
// updates BOTH maps in the same PR; that forced, explicit edit is the point. When
// the allowlist is empty both maps go to {} and the rule becomes a zero-allowlist
// Hard target.
func TestPermissionBasedAuthzAllowlist_Ceiling(t *testing.T) {
	assert.Equal(t, expectedPermissionBasedAuthzAllowlist, permissionBasedAuthzAllowlist,
		"permissionBasedAuthzAllowlist drifted from its frozen exact set — a new/removed/swapped entry "+
			"must update expectedPermissionBasedAuthzAllowlist in the SAME PR (migrate handlers to "+
			"auth.RequirePermission instead of adding exceptions; PR-10c drains the remaining 3)")
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

	// observed records every allowlisted file in which the scan saw a live
	// role-literal gate; the post-scan loop flags any allowlist entry NOT observed
	// as a STALE bypass slot (anti-vacuity — a migrated-away gate left in the
	// allowlist must not silently keep its exemption).
	observed := map[string]struct{}{}

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
				out = append(out, scanPermissionBasedAuthzViolations(p, f, rel, permissionBasedAuthzAllowlist, observed)...)
			}
			return out
		})

	// No-stale reverse self-check: every allowlist entry must have a live gate.
	for path := range permissionBasedAuthzAllowlist {
		if _, seen := observed[path]; !seen {
			diags = append(diags, Diagnostic{
				Rel: path,
				Message: "permissionBasedAuthzAllowlist entry " + path + " is STALE — no live " +
					"auth.AnyRole/SelfOr/RequireAnyRole gate observed there; the gate was migrated away or the " +
					"scanner regressed. Drop the dead allowlist entry so it cannot become a silent bypass slot",
			})
		}
	}

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
				diags = append(diags, scanPermissionBasedAuthzViolations(p, f, rel, emptyAllowlist, nil)...)
			}
			return nil
		})

	assert.GreaterOrEqual(t, len(diags), 1,
		"reverse fixture: expected ≥1 diagnostic for the role-literal auth.AnyRole callsite "+
			"in testdata/permission_based_authz_red — "+
			rulePermissionBasedAuthz01+" must fire, else the rule is vacuous")
}
