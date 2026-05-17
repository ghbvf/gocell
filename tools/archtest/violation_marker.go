package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// fixturespecViolationPkgPath / fixturespecViolationName identify the
// canonical (pkgPath, name) pair of the Violation marker function. Single
// source of truth across CountViolationMarkers + the
// FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01 funnel rule. ResolvePackageRef
// converges qualified selectors, dot imports, and aliases on this same pair.
const (
	fixturespecViolationPkgPath = "github.com/ghbvf/gocell/tools/archtest/fixturespec"
	fixturespecViolationName    = "Violation"
)

// CountViolationMarkers walks pass.Files and returns the number of
// *ast.CallExpr nodes whose callee resolves (via pass.TypesInfo) to
// fixturespec.Violation. Result is the canonical expected diagnostic count
// for the fixture pkg(s) bound to pass.
//
// Returns 0 when pass is nil or pass.TypesInfo is nil (an AST-only Pass
// cannot resolve callee identity through go/types).
//
// AI-rebust Hard: callee identity is established by ResolvePackageRef →
// (pkgPath, name) equality against the fixturespecViolation* constants
// above. Name aliasing, dot imports, and qualified selectors all converge
// on the same identity pair — see ResolvePackageRef godoc.
func CountViolationMarkers(pass *Pass) int {
	if pass == nil || pass.TypesInfo == nil {
		return 0
	}
	count := 0
	for _, f := range pass.Files {
		EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(pass.TypesInfo, call.Fun)
			if !ok {
				return
			}
			if pkgPath == fixturespecViolationPkgPath && name == fixturespecViolationName {
				count++
			}
		})
	}
	return count
}

// AssertDiagnosticCount asserts len(got) equals CountViolationMarkers(pass).
// On mismatch it reports both sets (with file:line for each got Diagnostic)
// via t.Errorf so the failure prints the actual diagnostics for triage.
// ruleID is included in the failure message.
//
// When got < want (rule produced fewer diagnostics than markers), the failure
// message also lists each spec.Violation() marker position that has no
// corresponding got Diagnostic, to help identify which fixture call sites
// the rule missed.
//
// Single, focused assertion: every fixture-loading test must route through
// this helper. Enforced by meta-archtest FIXTURESPEC-COUNT-MATCH-ENFORCED-01
// (upstream Hard).
func AssertDiagnosticCount(t testing.TB, ruleID string, pass *Pass, got []Diagnostic) {
	t.Helper()
	want := CountViolationMarkers(pass)
	if len(got) == want {
		return
	}
	sorted := append([]Diagnostic(nil), got...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Rel != sorted[j].Rel {
			return sorted[i].Rel < sorted[j].Rel
		}
		return sorted[i].Line < sorted[j].Line
	})
	var b strings.Builder
	for _, d := range sorted {
		fmt.Fprintf(&b, "\n    %s:%d: %s", d.Rel, d.Line, d.Message)
	}
	// When we have fewer diagnostics than markers, list marker positions so
	// the author can see which spec.Violation() calls the rule missed.
	if len(got) < want && pass != nil && pass.TypesInfo != nil && pass.Fset != nil {
		b.WriteString("\n  marker positions with no matching diagnostic:")
		for _, f := range pass.Files {
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				pkgPath, name, ok := ResolvePackageRef(pass.TypesInfo, call.Fun)
				if !ok || pkgPath != fixturespecViolationPkgPath || name != fixturespecViolationName {
					return
				}
				pos := pass.Fset.Position(call.Pos())
				fmt.Fprintf(&b, "\n    marker at %s:%d — no diagnostic", pos.Filename, pos.Line)
			})
		}
	}
	t.Errorf("%s: diagnostic count mismatch — got %d, want %d (markers via fixturespec.Violation in fixture pkg):%s",
		ruleID, len(got), want, b.String())
}

// NoDiagnosticAssertion is a typed opt-out marker for test functions that
// LOAD a fixture (via RunTypedDir / RunTypedFixture / RunTyped or Run with
// a testdata path) but DELIBERATELY do not assert against the rule's
// diagnostic output — e.g., framework-shape tests that verify Pass.Pkg /
// Pass.TypesInfo / Pass.Files plumbing rather than diagnostic content.
//
// Calling this in a test file satisfies FIXTURESPEC-COUNT-MATCH-ENFORCED-01
// in place of AssertDiagnosticCount. Use only for tests that genuinely
// do not bind to diagnostic output; misuse (silently exempting a real
// diagnostic-binding test) regresses the funnel to Soft.
//
// AI-rebust: this is a Hard typed marker (callee resolved via *types.Info)
// — the equivalent of "注释豁免 → typed marker" per charter §"Soft → Hard
// 改造方向". The reviewability burden shifts from the rule (no longer fires)
// to the marker call site (visible diff, named function).
//
// See fixturespec_funnel_test.go for the canonical meta-archtest usage
// (TestFixturespecViolationCallerAllowlist and TestFixturespecCountMatchEnforced
// each call NoDiagnosticAssertion() as they are funnel self-tests that verify
// the enforcement mechanism itself, not fixture diagnostic content).
//
// Runtime: deliberately a no-op.
func NoDiagnosticAssertion() {}
