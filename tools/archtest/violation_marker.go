package archtest

import "testing"

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
//
// STUB: Wave 1 returns -1 sentinel so the unit test fails RED until Wave 2.
func CountViolationMarkers(pass *Pass) int {
	if pass == nil || pass.TypesInfo == nil {
		return 0
	}
	return -1
}

// AssertDiagnosticCount asserts len(got) equals CountViolationMarkers(pass),
// reporting both sets (with file:line for each got Diagnostic) on mismatch.
// ruleID is included in the failure message for triage.
//
// Single, focused assertion: every fixture-loading test must route through
// this helper. Enforced by meta-archtest FIXTURESPEC-COUNT-MATCH-ENFORCED-01
// (upstream Hard).
//
// STUB: Wave 1 calls t.Fatalf with "not implemented" so callers RED until
// Wave 2.
func AssertDiagnosticCount(t testing.TB, ruleID string, pass *Pass, got []Diagnostic) {
	t.Helper()
	t.Fatalf("archtest.AssertDiagnosticCount: not implemented (Wave 1 stub; rule=%s, len(got)=%d)",
		ruleID, len(got))
}
