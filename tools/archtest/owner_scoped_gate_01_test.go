//go:build archtest

// INVARIANT: OWNER-SCOPED-GATE-EXACT-SET-01
//
// OWNER-SCOPED-GATE-EXACT-SET-01 freezes the set of owner-scoped route gates in
// accesscore. An owner-scoped endpoint (one whose resource ownership the PDP
// decides via the baseline rule subject.sub == resource.id, #1977) MUST gate with
//
//	auth.RequirePermissionForResource("<pathParam>", authz.Perm*())
//
// which canonicalizes the path-param resource id and forwards it to the PDP as the
// `resource` argument. The frozen expected set (ownerScopedGateExpectedSet) is the
// exact list of (handler, pathParam, permission-accessor) triples.
//
// The threat this closes (PR #2025 review F1): tenancy.md mandates this gate shape,
// but PERMISSION-BASED-AUTHZ-01 only BANS role-literal gates — it cannot detect an
// owner endpoint silently regressing from RequirePermissionForResource to plain
// auth.RequirePermission(perm). Plain RequirePermission forwards r.URL.Path (not the
// canonical resource id), so the ownership rule never matches and self-access breaks
// (or, with a permissive policy, widens). A regression drops the endpoint's triple
// from the collected set → exact-set mismatch → CI fails.
//
// # AI-robust grade: Medium
//
// Downstream Medium: type-aware CallExpr scan via ResolvePackageRef (alias/dot-import
// safe) collects every RequirePermissionForResource(strlit, authz.Perm*()) triple in
// the two guarded handler files and asserts the collected set EQUALS the frozen set.
// NOT Hard: nothing in the type system forces an owner endpoint to choose
// RequirePermissionForResource over RequirePermission (both type-check); only this
// scan rejects the wrong choice. Hard-ification end state: contract.yaml declares the
// owner-scoped gate → cellgen-derived gate + golden (Hard 范本 "codegen funnel + golden"),
// the same end state tracked for PERMISSION-BASED-AUTHZ-01 (PR-13).
//
// # Anti-vacuity
//
// This is a PRESENCE exact-set scan, not a ban scan: a green result PROVES the scanner
// works, because it can only be green when the scan collected exactly the frozen
// triples. Two built-in discriminators make it non-vacuous without a ban-style reverse
// fixture:
//
//   - identitymanage/handler.go contains BOTH auth.RequirePermission(authz.PermUserWrite())
//     (admin gate, line ~267) AND auth.RequirePermissionForResource("id", authz.PermUserWrite())
//     (owner gate). The scan collects ONLY the RequirePermissionForResource triple; if it
//     over-collected plain RequirePermission, an UNEXPECTED triple would break the exact set.
//   - If the typed scan silently failed to resolve any callsite, the collected set would be
//     empty and every frozen triple would report MISSING → fail.
//
// TestOwnerScopedGate_ReverseFixture additionally scans a standalone RED module that
// (a) drifts a gate's path param and (b) regresses an owner gate to plain
// RequirePermission, proving param drift is observed as a changed triple and a plain-gate
// regression is observed as a MISSING triple — the two real-world drift modes.
//
// # Blind spots
//
//   - Guards gate CONSTRUCTION, not route→gate WIRING: a correctly-constructed gate that
//     is never mounted (or mounted on the wrong handler) is not caught here — the
//     contract serve tests + e2e cover wiring.
//   - Only the two named handler files are scanned; a NEW owner-scoped endpoint in a new
//     file must be added to ownerScopedGateHandlerKey + ownerScopedGateExpectedSet (the
//     UNEXPECTED-triple check forces this consciously for the two guarded files).
package archtest

import (
	"go/ast"
	"go/constant"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const ruleOwnerScopedGateExactSet01 = "OWNER-SCOPED-GATE-EXACT-SET-01"

// authzImportPath is the canonical import path of the sealed permission registry
// (pkg/authz). The second argument of every owner-scoped gate must be a call to one
// of its Perm*() accessor functions.
const authzImportPath = PlatformModulePath + "/pkg/authz"

// ownerScopedGateExpectedSet is the FROZEN set of owner-scoped route gates, keyed
// "<handler>|<pathParam>|<permAccessor>". Adding/removing an owner endpoint, or
// changing its path param or permission, must update this set in the same change —
// otherwise the exact-set compare fails. See the file godoc for the invariant.
var ownerScopedGateExpectedSet = map[string]struct{}{
	"identitymanage|id|PermUserRead":  {},
	"identitymanage|id|PermUserWrite": {},
	"rbaccheck|userID|PermRoleRead":   {},
}

// ownerScopedGateHandlerKey maps a module-relative handler path to its short key,
// or "" if the file is not one of the owner-scoped handlers under guard.
func ownerScopedGateHandlerKey(rel string) string {
	switch {
	case strings.HasSuffix(rel, "slices/identitymanage/handler.go"):
		return "identitymanage"
	case strings.HasSuffix(rel, "slices/rbaccheck/handler.go"):
		return "rbaccheck"
	default:
		return ""
	}
}

// ownerScopedGateConstString returns the compile-time string value of expr (handles
// string literals and const-bound identifiers via go/types constant folding), or
// ("", false) if expr is not a constant string.
func ownerScopedGateConstString(p *Pass, expr ast.Expr) (string, bool) {
	tv, ok := p.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// collectOwnerScopedGates scans one file for auth.RequirePermissionForResource
// callsites, returning a "<handler>|<pathParam>|<permAccessor>" key for each. Only
// calls whose receiver resolves to runtime/auth and whose second argument is a call
// to a pkg/authz accessor are recognized; a non-const param or non-authz accessor is
// recorded with a sentinel so it surfaces as an UNEXPECTED triple rather than being
// silently skipped.
func collectOwnerScopedGates(p *Pass, f *ast.File, handler string) []string {
	var out []string
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != authRuntimeImportPath || name != "RequirePermissionForResource" {
			return
		}
		if len(call.Args) != 2 {
			out = append(out, handler+"|<bad-arity>|<bad-arity>")
			return
		}
		param, ok := ownerScopedGateConstString(p, call.Args[0])
		if !ok {
			param = "<non-const-param>"
		}
		accessor := "<non-authz-accessor>"
		if argCall, ok := call.Args[1].(*ast.CallExpr); ok {
			if apath, aname, ok := ResolvePackageRef(p.TypesInfo, argCall.Fun); ok && apath == authzImportPath {
				accessor = aname
			}
		}
		out = append(out, handler+"|"+param+"|"+accessor)
	})
	return out
}

// TestOwnerScopedGate_ExactSet_01 enforces OWNER-SCOPED-GATE-EXACT-SET-01: the
// collected set of owner-scoped RequirePermissionForResource triples across the
// guarded accesscore handler files must EQUAL ownerScopedGateExpectedSet.
func TestOwnerScopedGate_ExactSet_01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	collected := map[string]struct{}{}
	_ = Run(t, Typed(TypedOpts{}, []string{"./corecells/..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				handler := ownerScopedGateHandlerKey(rel)
				if handler == "" {
					continue
				}
				for _, k := range collectOwnerScopedGates(p, f, handler) {
					collected[k] = struct{}{}
				}
			}
			return nil
		})

	var diags []Diagnostic
	for k := range ownerScopedGateExpectedSet {
		if _, ok := collected[k]; !ok {
			diags = append(diags, Diagnostic{
				Message: "MISSING owner-scoped gate " + k + " — an owner endpoint regressed off " +
					"auth.RequirePermissionForResource(pathParam, authz.Perm*()) or changed its param/permission; " +
					"ownership would no longer be PDP-decided (" + ruleOwnerScopedGateExactSet01 + ")",
			})
		}
	}
	for k := range collected {
		if _, ok := ownerScopedGateExpectedSet[k]; !ok {
			diags = append(diags, Diagnostic{
				Message: "UNEXPECTED owner-scoped gate " + k + " — a new/changed owner endpoint must be added " +
					"to ownerScopedGateExpectedSet (the frozen set) in the same change (" + ruleOwnerScopedGateExactSet01 + ")",
			})
		}
	}
	Report(t, ruleOwnerScopedGateExactSet01, diags)
}

// TestOwnerScopedGate_ReverseFixture scans the standalone RED module and asserts the
// two real drift modes are observable: a path-param drift surfaces as a changed
// triple, and an owner gate regressed to plain auth.RequirePermission surfaces as a
// MISSING RequirePermissionForResource triple (it is not collected at all). Proves
// the production exact-set compare is not vacuous.
func TestOwnerScopedGate_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "owner_scoped_gate_red")

	collected := map[string]struct{}{}
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				for _, k := range collectOwnerScopedGates(p, f, "redfixture") {
					collected[k] = struct{}{}
				}
			}
			return nil
		})

	// Param drift is observed as a changed triple.
	assert.Contains(t, collected, "redfixture|wrongParam|PermUserWrite",
		"reverse fixture: a path-param drift must surface as a changed triple — "+
			ruleOwnerScopedGateExactSet01+" extraction is vacuous otherwise")
	// The correctly-shaped gate is collected.
	assert.Contains(t, collected, "redfixture|id|PermUserRead",
		"reverse fixture: a well-formed owner gate must be collected")
	// A plain RequirePermission owner gate (the regression) is NOT collected as a
	// RequirePermissionForResource triple → in production it would be a MISSING entry.
	assert.NotContains(t, collected, "redfixture|userID|PermRoleRead",
		"reverse fixture: a regression to plain auth.RequirePermission must NOT be collected as an owner gate (so production reports it MISSING)")
	for k := range collected {
		assert.False(t, strings.Contains(k, "PermRoleRead"),
			"reverse fixture: the plain RequirePermission(PermRoleRead) gate must not produce any owner-gate triple, got %q", k)
	}
}
