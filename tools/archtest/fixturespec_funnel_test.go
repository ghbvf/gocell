// INVARIANT: FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01
//   - INVARIANT: FIXTURESPEC-COUNT-MATCH-ENFORCED-01
//
// fixturespec_funnel_test.go — funnel double-lock for the fixturespec.Violation
// typed marker.
//
//   - Downstream Hard (CALLER-ALLOWLIST-01): callers of fixturespec.Violation
//     must reside in fixture .go files under tools/archtest/testdata/. Any
//     CallExpr resolving (via *types.Info) to fixturespec.Violation outside
//     testdata/ is a violation. Hard form: (callee resolved via
//     *types.Info, file location filter) — identity check, not name match.
//
//   - Upstream Medium (COUNT-MATCH-ENFORCED-01): regression guard against
//     the *specific* hardcoded-fixture-line-number anti-pattern. Fires only
//     on files that combine BOTH (a) a Run/RunTyped/RunTypedDir/RunTypedFixture
//     callee resolved via *types.Info AND (b) a struct field named one of
//     wantLines/wantLine/wantViolLine/wantViolLines/expectedLine/expectedLines
//     with element type int. If both, file must contain a CallExpr resolved
//     via *types.Info to archtest.AssertDiagnosticCount OR archtest.NoDiagnosticAssertion.
//
//     Honest rating: this combined-trigger form is **Medium upstream**, not
//     Hard — the field-name component is Soft (name convention) per charter
//     §"Soft → Hard 改造方向". It is a transitional regression guard targeted
//     at the originally-observed 10 files; broader Hard upstream coverage
//     (every fixture-loading test must call the assertion or opt-out) is
//     tracked as backlog item FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01.
//     The downstream Hard + Medium upstream combination is the explicitly-
//     allowed transitional pattern per charter §"Funnel 双向锁评级"; the
//     backlog reference here ties the upgrade path to a named follow-up.
//
// Self-exempt: this funnel file has Run callees + a "testdata" literal but
// lacks any wantLines-style int field — naturally not triggered. The
// downstream rule above also calls NoDiagnosticAssertion() at the top of
// each test func, redundantly proving the typed-marker opt-out path works
// and serving as a smoke-check for COUNT-MATCH detection.
//
// ref: .claude/rules/gocell/ai-collab.md §"Hard 范本" entries 2 & 4
//
//	.claude/rules/gocell/ai-collab.md §"Funnel 双向锁评级"
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
	noDiagnosticAssertionName    = "NoDiagnosticAssertion"
)

// hardcodedLineFieldNames is the closed set of field names that constitute
// the legacy "hardcoded fixture line number" anti-pattern. Detection by name
// is Soft per charter; the rule is a regression guard, not a Hard upstream
// enforcement (which is tracked separately as backlog
// FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01).
var hardcodedLineFieldNames = map[string]bool{
	"wantLines":     true,
	"wantLine":      true,
	"wantViolLine":  true,
	"wantViolLines": true,
	"expectedLine":  true,
	"expectedLines": true,
}

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
	NoDiagnosticAssertion() // funnel meta-archtest; not fixture-binding

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

// TestFixturespecCountMatchEnforced enforces COUNT-MATCH-ENFORCED-01 with
// targeted detection of the legacy hardcoded-line anti-pattern.
//
// Detection (per *_test.go file in tools/archtest/, AND'd):
//
//   - File contains any CallExpr resolving (via *types.Info) to
//     archtest.RunTypedDir / RunTypedFixture / Run / RunTyped — fixture
//     loader callee in scope.
//   - File contains any *ast.StructType with at least one field whose name
//     ∈ hardcodedLineFieldNames AND whose Type is `[]int` (ArrayType with
//     Elt = Ident "int", no Len) — the regression target.
//
// Satisfaction: file must contain a CallExpr resolving to
// archtest.AssertDiagnosticCount or archtest.NoDiagnosticAssertion.
//
// AI-rebust: Medium upstream (Hard callee detection AND Soft field-name
// match combined). Charter §"Funnel 双向锁评级" allows Medium upstream +
// Hard downstream as transitional with backlog upgrade — tracked at
// FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01.
func TestFixturespecCountMatchEnforced(t *testing.T) {
	t.Parallel()
	NoDiagnosticAssertion() // funnel meta-archtest; not fixture-binding

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
			if !fileHasFixtureLoader(p.TypesInfo, f) {
				continue
			}
			hardcodedField, hardcodedFieldNode := findHardcodedLineField(f)
			if hardcodedField == "" {
				continue
			}
			if fileHasAssertOrOptOut(p.TypesInfo, f) {
				continue
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(hardcodedFieldNode.Pos()).Line,
				Message: fmt.Sprintf(
					"file uses hardcoded line int field %q in a fixture-binding test "+
						"(loader callee present) but does not call archtest.AssertDiagnosticCount "+
						"or archtest.NoDiagnosticAssertion — migrate to fixturespec.Violation",
					hardcodedField),
			})
		}
		return diags
	}

	diags := RunTyped(t, TypedOpts{Tests: true}, []string{"./tools/archtest/..."}, rule)
	Report(t, "FIXTURESPEC-COUNT-MATCH-ENFORCED-01", diags)
}

// fileHasFixtureLoader reports whether f contains at least one CallExpr
// whose callee resolves (via info) to one of the four archtest fixture-load
// entries (Run / RunTyped / RunTypedDir / RunTypedFixture).
func fileHasFixtureLoader(info *types.Info, f *ast.File) bool {
	var found bool
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if found {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != archtestPkgPath {
			return
		}
		switch name {
		case fixtureLoaderRun, fixtureLoaderRunTyped,
			fixtureLoaderRunTypedDir, fixtureLoaderRunTypedFixture:
			found = true
		}
	})
	return found
}

// findHardcodedLineField walks f for *ast.StructType field declarations
// whose name ∈ hardcodedLineFieldNames AND whose Type is `[]int`. Returns
// the matched name + the field's *ast.Field node, or ("", nil) if absent.
// Caller derives line via fset.Position(node.Pos()).Line.
//
// The `[]int` shape check accepts only the literal slice-of-int form
// (ArrayType with no Len + Ident Elt "int"). Aliased types (`type Lines
// []int`; `wantLines Lines`) are intentionally not matched — broader
// detection is the Hard upgrade tracked in backlog
// FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01.
func findHardcodedLineField(f *ast.File) (string, *ast.Field) {
	var name string
	var node *ast.Field
	EachInSubtree[ast.StructType](f, func(st *ast.StructType) {
		if name != "" || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !isIntSliceType(field.Type) {
				continue
			}
			for _, ident := range field.Names {
				if hardcodedLineFieldNames[ident.Name] {
					name = ident.Name
					node = field
					return
				}
			}
		}
	})
	return name, node
}

// isIntSliceType reports whether expr denotes the literal `[]int` type
// (ArrayType with no Len + element type Ident "int").
func isIntSliceType(expr ast.Expr) bool {
	arr, ok := expr.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	ident, ok := arr.Elt.(*ast.Ident)
	return ok && ident.Name == "int"
}

// fileHasAssertOrOptOut reports whether f contains a CallExpr whose callee
// resolves to archtest.AssertDiagnosticCount or archtest.NoDiagnosticAssertion.
func fileHasAssertOrOptOut(info *types.Info, f *ast.File) bool {
	var ok bool
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if ok {
			return
		}
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		if !resolved || pkgPath != archtestPkgPath {
			return
		}
		if name == assertDiagnosticCountName || name == noDiagnosticAssertionName {
			ok = true
		}
	})
	return ok
}
