// Package fixturespec exposes a single typed marker function that archtest
// fixture .go files call to declare expected diagnostics from the rule
// under test. The package is intentionally microscopic — it exists only so
// fixture authors have ONE typed callable whose callsite count is the sole
// expression of "how many diagnostics this fixture should produce".
//
// Usage in a fixture .go file:
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
//     callers of fixturespec.Violation must live in a file with
//     //go:build archtest_fixture under tools/archtest/testdata/.
//
//   - FIXTURESPEC-COUNT-MATCH-ENFORCED-01 (upstream Hard):
//     every fixture-loading test func (calls archtest.RunTypedDir /
//     RunTypedFixture, or RunTyped/Run with a "testdata" path arg) must
//     contain a callee resolving to archtest.AssertDiagnosticCount.
package fixturespec

// Violation marks the call site as expecting exactly one diagnostic from
// the rule under test. Call once per expected diagnostic; CountViolationMarkers
// returns the total across all files in the Pass.
//
// Runtime: deliberately a no-op. The only effect is at AST time —
// archtest.CountViolationMarkers resolves *ast.CallExpr.Fun via *types.Info
// and counts hits.
func Violation() {}
