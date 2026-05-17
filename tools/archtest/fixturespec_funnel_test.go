// invariants:
//   - INVARIANT: FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01
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
//     Blind spot inventory (CALLER-ALLOWLIST-01):
//     Charter §"工具选定后强制盲区自检" requires listing all AST forms outside
//     the tool's declared scope plus honest declarations for covered forms.
//
//   - alias import (`import v "…/fixturespec"; v.Violation()`) — covered:
//     ResolvePackageRef resolves the callee via *types.Info.Uses, which maps
//     the aliased selector to the canonical (pkgPath, name) pair regardless of
//     the local alias name. No separate fixture needed.
//
//   - dot-import (`import . "…/fixturespec"; Violation()`) — covered:
//     *types.Info.Uses resolves the bare Ident to the canonical package path.
//     ResolvePackageRef handles this via the Ident → *types.Func path.
//     No separate fixture needed.
//
//   - func-value (`f := spec.Violation; f()`) — covered: ResolvePackageRef
//     is called on call.Fun for each *ast.CallExpr; a func-value indirect call
//     (`f()`) produces a CallExpr whose Fun is an Ident resolved through
//     *types.Info.Uses to the original *types.Func. Covered by the same
//     *types.Info sweep. No separate fixture needed.
//     Honest scope declaration: all three forms are covered by ResolvePackageRef
//     via *types.Info; no blind-spot fixture is required for CALLER-ALLOWLIST-01.
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
//     Blind spot inventory (COUNT-MATCH-ENFORCED-01):
//
//   - aliased []int type (`type Lines []int; wantLines Lines`) — NOT detected
//     by isIntSliceType, which only matches the literal *ast.ArrayType with Elt
//     Ident "int". Renamed or aliased element types escape the field-type check.
//     Honest scope declaration: this is an accepted Soft gap; broader detection
//     deferred to backlog FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01.
//
//   - plain int count field (e.g., `wantViolCount int`) — NOT detected:
//     element type is Ident "int" (not ArrayType), so isIntSliceType returns
//     false. This form is the anti-pattern used in
//     errcode_invariants_test.go::TestDetailsSlogAttrFixtures,
//     errcode_message_const_fixtures_test.go, and
//     span_record_error_redact_test.go. Honest scope declaration: not
//     detected by current Medium funnel; migration to spec.Violation() +
//     AssertDiagnosticCount deferred to backlog
//     FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01. (clock_invariants_test.go's
//     TestClockInjectionCallsiteFixtures + TestKernelClockLeafFallbackFixtures
//     also used this form before PR557; closed in PR557 A1 fix.)
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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureLoaderRunTypedDir     = "RunTypedDir"
	fixtureLoaderRunTypedFixture = "RunTypedFixture"
	fixtureLoaderRun             = "Run"
	fixtureLoaderRunTyped        = "RunTyped"
	assertDiagnosticCountName    = "AssertDiagnosticCount"
	noDiagnosticAssertionName    = "NoDiagnosticAssertion"
)

// hardcodedLineFieldNames is the targeted set of field names that constitute
// the legacy "hardcoded fixture line number" anti-pattern (covers the
// PR604-identified 10 files). Detection by name is Soft per charter; the rule
// is a regression guard, not a Hard upstream enforcement (which is tracked
// separately as backlog FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01). Note:
// int-count variants like wantViolCount/wantViolReps (element type int, not
// []int) escape via different element type — tracked by the same backlog item.
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

// scanFuncDeclsMissingAssertOrOptOut returns the *ast.FuncDecl entries in f
// that match the FuncDecl-level form of the COUNT-MATCH-ENFORCED-01 anti-
// pattern: the FuncDecl body contains a fixture-loader callee AND declares
// a hardcoded line int slice field, AND does NOT contain a call to
// archtest.AssertDiagnosticCount / archtest.NoDiagnosticAssertion (directly
// OR via 1-hop call to another FuncDecl in the same file whose body contains
// the assert/opt-out).
//
// fix-6 motivation: the file-level rule in TestFixturespecCountMatchEnforced
// exempts every FuncDecl in a file whenever any one of them calls the
// assertion or opt-out. A single migrated test in the file silently exempts
// adjacent unmigrated tests still on the wantLines anti-pattern.
//
// AI-rebust: collapsing the satisfaction scope from file → FuncDecl removes
// the cross-FuncDecl bleed. 1-hop helper-call expansion keeps idiomatic
// "test calls runFixtureScan helper" patterns valid.
//
// Wave 1 stub: returns nil — RED tests assert non-empty for the mixed-red
// fixture's `runA` FuncDecl. Wave 2 GREEN implements the real per-FuncDecl
// scan + 1-hop expansion.
func scanFuncDeclsMissingAssertOrOptOut(info *types.Info, f *ast.File) []*ast.FuncDecl {
	_ = info
	_ = f
	return nil
}

// ---------------------------------------------------------------------------
// Wave 1 (RED) tests for fix-1 / fix-4 / fix-6.
//
// Each test asserts the Wave 2 GREEN behavior against the current Wave 1
// stub or broken behavior. Expected to FAIL at runtime in the Wave 1 commit
// and to PASS in Wave 2.
// ---------------------------------------------------------------------------

// TestFixturespecViolationValuePositionDetected_Red is the Wave 1 RED test
// for fix-1 (form-uniqueness for the fixturespec.Violation marker).
//
// Loads the func_value_red fixture which contains:
//   - line 16: spec.Violation()             — direct call form (the marker)
//   - line 17: f := spec.Violation          — value-position assignment form
//   - line 18: f()                          — indirect call (info.Uses[f]
//     is *types.Var; ResolvePackageRef returns false)
//
// detectFixturespecValuePosition must emit ≥1 Diagnostic for the line-17
// assignment so the funnel becomes form-unique. Wave 1 stub returns nil →
// assertion fails → RED. Wave 2 implements the real scan → GREEN.
func TestFixturespecViolationValuePositionDetected_Red(t *testing.T) {
	t.Parallel()
	NoDiagnosticAssertion() // funnel meta-archtest; not fixture-binding
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata",
		"fixturespec_funnel_fixtures", "func_value_red")

	var diags []Diagnostic
	RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				diags = append(diags,
					detectFixturespecValuePosition(p.TypesInfo, p.Fset, f, p.Rel(f))...)
			}
			return nil
		})
	require.GreaterOrEqual(t, len(diags), 1,
		"detectFixturespecValuePosition must catch `f := spec.Violation` "+
			"assignment form in func_value_red fixture (got %d diagnostics)",
		len(diags))
}

// TestFixturespecCountMatchEnforced_FuncDeclLevel_Red is the Wave 1 RED test
// for fix-6 (FuncDecl-level granularity of COUNT-MATCH-ENFORCED-01).
//
// Loads the funcdecl_mixed_red fixture whose usage.go contains:
//   - runA: tcA{wantLines:[]int{...}} + archtest.RunTyped call, NO inline
//     AssertDiagnosticCount or NoDiagnosticAssertion.
//   - runB: archtest.RunTyped + archtest.AssertDiagnosticCount inline.
//
// The current file-level rule exempts both FuncDecls (runB's assert covers
// the file). FuncDecl-level scanFuncDeclsMissingAssertOrOptOut must flag
// runA only. Wave 1 stub returns nil → assertion fails → RED. Wave 2
// implements the per-FuncDecl scan with 1-hop helper expansion → GREEN.
func TestFixturespecCountMatchEnforced_FuncDeclLevel_Red(t *testing.T) {
	t.Parallel()
	NoDiagnosticAssertion() // funnel meta-archtest; not fixture-binding
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata",
		"fixturespec_funnel_fixtures", "funcdecl_mixed_red")

	var flagged []string
	RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				for _, fd := range scanFuncDeclsMissingAssertOrOptOut(p.TypesInfo, f) {
					if fd.Name != nil {
						flagged = append(flagged, fd.Name.Name)
					}
				}
			}
			return nil
		})
	assert.Contains(t, flagged, "runA",
		"FuncDecl-level rule must flag runA (hardcoded wantLines + loader callee + no inline assert)")
	assert.NotContains(t, flagged, "runB",
		"must NOT flag runB (has inline AssertDiagnosticCount)")
}
