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
// every call regardless of import alias; CI fails loud on any role-literal gate
// in any corecells business handler. The rule is NOT Hard because auth.AnyRole /
// auth.SelfOr remain callable (examples still use them pending PR-10d) so the
// constraint cannot be expressed as a compile error — a corecells handler can
// still type-check while calling them; only the typed scan rejects it.
//
// Upstream funnel: the allowlist is a migration ledger, now DRAINED to empty by
// PR-10c — the rule is a zero-exception scan over all corecells handlers.
// Hard-ification end state: permission declared in contract.yaml → cellgen-derived
// gate + golden (Hard 范本 "codegen funnel + golden"), tracked for PR-13.
//
// Funnel 双向锁评级 (ai-robust.md §"Funnel 双向锁评级"):
//
//	下游 Medium — archtest typed callsite scan, fail-loud in CI;
//	  alias-safe (ResolvePackageRef resolves the import path, not the local name).
//	上游 — allowlist convergence COMPLETE: PR-10a auditquery, PR-10b configcore,
//	  PR-10c accesscore all migrated to auth.RequirePermission /
//	  auth.RequirePermissionOrSelf; the allowlist is empty.
//	Hard-ification tracker: permission-gate codegen funnel → gh #PR-13.
//
// # Allowlist (empty)
//
// The migration ledger is EMPTY as of PR-10c (#1348): every corecells business
// handler (auditquery, the 5 configcore slices, the 3 accesscore slices) uses a
// permission gate and must not regress. Any auth.AnyRole/SelfOr/RequireAnyRole
// call in a corecells handler is now an unconditional violation.
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
// fires on testdata/permission_based_authz_red, proving the rule is not vacuous —
// the only anti-vacuity needed now that the allowlist is empty (with no
// allowlisted files, the production scan over ./corecells/... is itself non-vacuous
// only if it can detect a violation, which the reverse fixture confirms).
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

// permissionBasedAuthzAllowlist is the migration ledger: handler files that still
// legitimately call role-literal gates pending their PR-10x migration. Paths are
// module-relative slash paths (p.Rel values from the corecells module).
//
// EMPTY as of PR-10c (#1348): auditquery (PR-10a), the 5 configcore slices
// (PR-10b) and now the 3 accesscore slices (PR-10c) are all migrated to
// auth.RequirePermission / auth.RequirePermissionOrSelf. With an empty allowlist
// the rule is a ZERO-EXCEPTION scan: ANY auth.AnyRole/SelfOr/RequireAnyRole gate
// in a corecells business handler is a violation. The exact-set freeze + ceiling
// test were deleted with the last entry (an empty map cannot grow/swap silently —
// any re-addition is itself the diff a reviewer sees). Hard-ification
// (contract.yaml → cellgen gate + golden) is tracked for PR-13.
var permissionBasedAuthzAllowlist = map[string]struct{}{}

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
