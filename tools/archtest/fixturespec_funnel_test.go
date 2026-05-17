// INVARIANT: FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01
//   - INVARIANT: FIXTURESPEC-COUNT-MATCH-ENFORCED-01
//
// fixturespec_funnel_test.go — Hard funnel double-lock for the
// fixturespec.Violation typed marker.
//
//   - Downstream Hard (CALLER-ALLOWLIST-01): callers of fixturespec.Violation
//     must reside in fixture .go files under tools/archtest/testdata/. Any
//     CallExpr resolving (via *types.Info) to fixturespec.Violation outside
//     testdata/ is a violation. Hard form: (callee resolved via
//     *types.Info, file location filter) — identity check, not name match.
//
//   - Upstream Hard (COUNT-MATCH-ENFORCED-01): every test func in
//     tools/archtest/*_test.go that LOADS a fixture (calls
//     archtest.RunTypedDir, archtest.RunTypedFixture, or
//     archtest.Run/RunTyped with an argument whose EvaluateConstString
//     contains "testdata") must also call archtest.AssertDiagnosticCount in
//     the same FuncDecl subtree. Hard form: (callee resolved via *types.Info
//     to the fixture-load funnel) AND (callee resolved via *types.Info to
//     AssertDiagnosticCount) — isomorph of charter §"Hard 范本" entry 4
//     ((callee, arg) pair form-uniqueness).
//
// Wave 1 (RED): the second rule reports the 10 fixture tests still using
// the wantLines/wantViolLine pattern. Wave 3 migration drives that count
// to 0.
//
// ref: .claude/rules/gocell/ai-collab.md §"Hard 范本" entries 2 & 4
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	fixtureLoaderRunTypedDir     = "RunTypedDir"
	fixtureLoaderRunTypedFixture = "RunTypedFixture"
	fixtureLoaderRun             = "Run"
	fixtureLoaderRunTyped        = "RunTyped"
	assertDiagnosticCountName    = "AssertDiagnosticCount"
)

// TestFixturespecViolationCallerAllowlist enforces CALLER-ALLOWLIST-01.
// Scans the whole main module + test variants; any CallExpr whose callee
// resolves to fixturespec.Violation is a violation unless the enclosing
// file's module-relative path is under tools/archtest/testdata/.
//
// Note: testdata/ packages are normally excluded by go/packages "./..."
// pattern, so this rule's positive matches in the main-module scan are
// always violations. The path filter is a defensive check; it costs nothing
// and documents the intent.
func TestFixturespecViolationCallerAllowlist(t *testing.T) {
	t.Parallel()

	rule := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var diags []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasPrefix(rel, "tools/archtest/testdata/") {
				continue // legitimate fixture caller
			}
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
				if !ok || pkgPath != fixturespecViolationPkgPath || name != fixturespecViolationName {
					return
				}
				line := p.Fset.Position(call.Pos()).Line
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"fixturespec.Violation called outside tools/archtest/testdata/ (rel=%s)",
						rel),
				})
			})
		}
		return diags
	}

	diags := RunTyped(t, TypedOpts{Tests: true}, []string{"./..."}, rule)
	Report(t, "FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01", diags)
}

// TestFixturespecCountMatchEnforced enforces COUNT-MATCH-ENFORCED-01.
// Scans tools/archtest/*_test.go for fixture-loading test funcs; each such
// func must also call archtest.AssertDiagnosticCount in the same FuncDecl
// subtree.
//
// Trigger taxonomy (any one suffices):
//
//   - CallExpr callee resolves to archtest.RunTypedDir  (always fixture-binding)
//   - CallExpr callee resolves to archtest.RunTypedFixture (always fixture-binding)
//   - CallExpr callee resolves to archtest.Run OR archtest.RunTyped AND any
//     argument subtree contains an Expr whose EvaluateConstString result
//     contains the substring "testdata"
//
// Requirement: same FuncDecl subtree contains a CallExpr whose callee
// resolves to archtest.AssertDiagnosticCount.
//
// Wave 1 RED: the 10 legacy fixture tests (notfound_test_strict /
// panic_invariants / prod_clock_injection_fixtures / prod_duration_fixtures /
// clock_invariants / errcode_invariants / eval_predicate_centralization /
// exported_error_new_fixtures / implements_funnel /
// test_time_literal_fixtures) all match a trigger and do not call
// AssertDiagnosticCount. This test must FAIL with at least 10 diagnostics
// until Wave 3 completes.
func TestFixturespecCountMatchEnforced(t *testing.T) {
	t.Parallel()

	rule := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var diags []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if !strings.HasPrefix(rel, "tools/archtest/") || !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
					return
				}
				trigger, hasAssert := scanFixtureFuncBody(p.TypesInfo, fn.Body)
				if !trigger || hasAssert {
					return
				}
				line := p.Fset.Position(fn.Pos()).Line
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"test %s loads a fixture (RunTypedDir/RunTypedFixture or Run/RunTyped with testdata arg) but does not call archtest.AssertDiagnosticCount",
						fn.Name.Name),
				})
			})
		}
		return diags
	}

	diags := RunTyped(t, TypedOpts{Tests: true}, []string{"./tools/archtest/..."}, rule)
	Report(t, "FIXTURESPEC-COUNT-MATCH-ENFORCED-01", diags)
}

// scanFixtureFuncBody walks body and reports whether it (a) triggers the
// fixture-loading rule and (b) contains an AssertDiagnosticCount call.
// Extracted so the rule body stays under the project's gocognit budget.
func scanFixtureFuncBody(info *types.Info, body *ast.BlockStmt) (trigger, hasAssert bool) {
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != archtestPkgPath {
			return
		}
		switch name {
		case fixtureLoaderRunTypedDir, fixtureLoaderRunTypedFixture:
			trigger = true
		case fixtureLoaderRun, fixtureLoaderRunTyped:
			if anyArgRefsTestdata(info, call.Args) {
				trigger = true
			}
		case assertDiagnosticCountName:
			hasAssert = true
		}
	})
	return trigger, hasAssert
}

// anyArgRefsTestdata returns true if any expression in args (or any
// sub-expression reachable from it) resolves via EvaluateConstString to a
// string containing the substring "testdata". Detects literal patterns
// like "./tools/archtest/testdata/foo" and filepath.Join const-folded
// equivalents.
func anyArgRefsTestdata(info *types.Info, args []ast.Expr) bool {
	for _, arg := range args {
		if checkExprForTestdata(info, arg) {
			return true
		}
	}
	return false
}

// checkExprForTestdata recursively probes expr (and BasicLit / Ident /
// SelectorExpr / BinaryExpr sub-nodes) for an EvaluateConstString match
// against "testdata".
func checkExprForTestdata(info *types.Info, expr ast.Expr) bool {
	if v, ok := EvaluateConstString(info, expr); ok && strings.Contains(v, "testdata") {
		return true
	}
	// Composite literals (e.g., []string{"./tools/archtest/testdata/..."}) —
	// inspect each element.
	if cl, ok := expr.(*ast.CompositeLit); ok {
		for _, elt := range cl.Elts {
			if checkExprForTestdata(info, elt) {
				return true
			}
		}
	}
	// CallExpr (filepath.Join(root, "tools", "archtest", "testdata", ...))
	// — inspect each arg for substring matches.
	if call, ok := expr.(*ast.CallExpr); ok {
		for _, sub := range call.Args {
			if checkExprForTestdata(info, sub) {
				return true
			}
		}
	}
	return false
}
