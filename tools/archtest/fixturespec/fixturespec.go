// Package fixturespec exposes a single typed marker function that archtest
// fixture .go files call to declare expected diagnostics from the rule
// under test. The package is intentionally microscopic — it exists only so
// fixture authors have ONE typed callable whose callsite count is the sole
// expression of "how many diagnostics this fixture should produce".
//
// Usage in a fixture .go file:
//
// There are two fixture-loading patterns, each with a different build-tag
// requirement:
//
// Pattern A — RunTypedFixture (fixture shares main module, own sub-package):
// The fixture .go file must carry the build tag so it is excluded from
// normal ./... builds but included when RunTypedFixture passes the tag:
//
//	//go:build archtest_fixture
//	package mypanicfixture
//
//	import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
//
//	func bad() {
//	    spec.Violation()  // expect one diagnostic from the rule under test
//	    panic("foo")
//	}
//
// Pattern B — RunTypedDir (fixture has its own go.mod, standalone module):
// The fixture .go file does NOT need the build tag because RunTypedDir loads
// the fixture directory as a standalone module; it is never part of the main
// module's ./... graph:
//
//	package mypanicfixture
//
//	import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
//
//	func bad() {
//	    spec.Violation()  // expect one diagnostic from the rule under test
//	    panic("foo")
//	}
//
// Most fixtures added in PR604 use Pattern B (RunTypedDir with own go.mod).
// Use Pattern A only when the fixture must share types with the main module.
//
// Companion: the archtest test file calls archtest.AssertDiagnosticCount
// after running the rule against the fixture; that helper counts callees
// resolving to fixturespec.Violation (via *types.Info) and asserts
// len(got) == count.
//
// AI-rebust: Hard (typed function call funnel). The (callee resolved via
// *types.Info to fixturespec.Violation) form is unique — no string anchor,
// no comment marker, no name convention. See charter §"Hard 范本" entry 2
// (panic(panicregister.Approved) Hard funnel isomorph). Locking is done by
// two meta-archtest rules in tools/archtest/fixturespec_funnel_test.go:
//
//   - FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01 (downstream Hard):
//     callers of fixturespec.Violation must live in fixture .go files under
//     tools/archtest/testdata/. The build tag is NOT part of the enforcement
//     predicate — path location is the binding check. RunTypedDir-loaded
//     fixtures (standalone go.mod, no build tag) satisfy this equally.
//
//   - FIXTURESPEC-COUNT-MATCH-ENFORCED-01 (upstream Medium):
//     every fixture-loading test func (calls archtest.RunTypedDir /
//     RunTypedFixture, or RunTyped/Run with a "testdata" path arg) that also
//     has a wantLines-style []int field must contain a callee resolving to
//     archtest.AssertDiagnosticCount or archtest.NoDiagnosticAssertion.
package fixturespec

// Violation marks the call site as expecting exactly one diagnostic from
// the rule under test. Call once per expected diagnostic; CountViolationMarkers
// returns the total across all files in the Pass.
//
// Runtime: deliberately a no-op. The only effect is at AST time —
// archtest.CountViolationMarkers resolves *ast.CallExpr.Fun via *types.Info
// and counts hits.
func Violation() {}
