// authz_eval_attr_notfound_guard_test.go — guards the fail-closed attribute
// lookup in the ABAC PDP engine: a resolve() callsite must not discard the
// found bool.
//
//   - INVARIANT: AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01
//
// # What this guards
//
// attributeResolver.resolve(source, key) returns (vals []string, found bool).
// found=false means the attribute is unavailable from a trusted source; the
// evaluator MUST treat that as an unsatisfied condition (fail-closed). Discarding
// found — `vals, _ := r.resolve(...)` — and then matching on the (empty) vals is
// a fail-OPEN bug: a negative operator (not_in / neq) over an empty value set
// would spuriously satisfy a condition and could grant access on a missing
// attribute. This archtest forbids discarding the found result at any resolve
// callsite in the engine package.
//
// # AI-robust rating (Medium, single axis)
//
// Detector is type-aware (go/types info.Uses resolves the selector to the
// authorizationdecide attributeResolver.resolve *types.Func; import alias is
// irrelevant since the receiver/pkg identity is matched, not the textual name).
// Rated Medium: it is an archtest, not a type-system seal. Hard-upgrade path =
// make resolve return a sealed lookup type whose value can only be consumed via a
// method that forces the found check (YAGNI for one callsite today; trigger =
// a second resolver consumer appears). Tracked at gh #1738.
//
// # Detection + anti-vacuity
//
// Anti-vacuity: the production scan asserts at least one attributeResolver.resolve
// callsite exists, so the rule cannot pass vacuously if the method is renamed or
// the only callsite removed. A RED fixture (internal/authzattrfixture, gated
// behind archtest_fixture) discards the found bool and the reverse self-check
// asserts the detector fires.
//
// # Tool blind spot (charter §"强制盲区自检")
//
//   - Only `:=` / `=` AssignStmt forms are scanned; a `var vals, _ = r.resolve(...)`
//     GenDecl form is not (the engine uses `:=` exclusively). Resolving through an
//     intermediate variable that then drops found is also out of scope — the same
//     data-flow limit as every callsite-form archtest in this suite.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authzDecidePkgPath is the authorizationdecide engine package import path.
const authzDecidePkgPath = PlatformCellsModulePath + "/accesscore/slices/authorizationdecide"

// TestAuthzEvalAttrNotFoundGuard01 asserts no engine resolve() callsite discards
// the found bool, and that at least one resolve callsite exists (anti-vacuity).
func TestAuthzEvalAttrNotFoundGuard01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var callsites int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanResolveFoundDiscard(p, authzDecidePkgPath, &callsites)
	})
	if callsites == 0 {
		diags = append(diags, Diagnostic{
			Message: "AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01: VACUOUS — no attributeResolver.resolve callsite observed. " +
				"The method was renamed or its only callsite removed; the fail-closed guard is no longer live.",
		})
	}
	Report(t, "AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01", diags)
}

// scanResolveFoundDiscard flags any AssignStmt whose RHS is a call to the resolve
// method of pkgPath and whose 2nd LHS (found) is the blank identifier. callsites
// counts every resolve call seen (for anti-vacuity).
func scanResolveFoundDiscard(p *Pass, pkgPath string, callsites *int) []Diagnostic {
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
			if len(as.Rhs) != 1 {
				return
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			fn, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
			if !ok || fn.Name() != "resolve" {
				return
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok || sig.Recv() == nil {
				return
			}
			if fn.Pkg() == nil || fn.Pkg().Path() != pkgPath {
				return
			}
			*callsites++
			if len(as.Lhs) != 2 {
				return
			}
			if blank, ok := as.Lhs[1].(*ast.Ident); ok && blank.Name == "_" {
				pos := p.Fset.Position(as.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01: attributeResolver.resolve found bool discarded in %s. A "+
							"missing attribute (found=false) must fail the condition closed; discarding found makes a "+
							"missing attribute fail OPEN under negative operators. Bind and check found.", rel,
					),
				})
			}
		})
	}
	return d
}

// TestAuthzEvalAttrNotFoundGuard01_RedFixture is the reverse self-check: the
// fixture discards the found bool; the detector must flag it.
func TestAuthzEvalAttrNotFoundGuard01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	fixturePkg := modPath + "/tools/archtest/internal/authzattrfixture"
	pattern := "./tools/archtest/internal/authzattrfixture/..."

	var dummy, found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		found += len(scanResolveFoundDiscard(p, fixturePkg, &dummy))
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: detector must flag the fixture's discarded found bool")
}
