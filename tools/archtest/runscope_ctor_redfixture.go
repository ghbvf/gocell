//go:build archtest_fixture

// This file is an in-package RED fixture for RUNSCOPE-CONSTRUCTOR-FUNNEL-01,
// gated by the archtest_fixture build tag so it is invisible to the default
// `go build` / `go test` / live archtest scan (which loads tags=nil). It is
// loaded only by TestRunScopeConstructorFunnel01_FixtureCoverage via
// Run(t, Fixture(FixtureOpts{Tests: false}, []string{"./tools/archtest"}), …).
//
// # Why this fixture must live in package archtest
//
// The bypass RUNSCOPE-CONSTRUCTOR-FUNNEL-01 closes is a business *_test.go in
// package archtest constructing one of the UNEXPORTED RunScope structs directly
// — e.g. typedRunScope{opts: TypedOpts{Tags: []string{"archtest_fixture"}}, …} —
// sidestepping both the sealed-RunScope downstream Hard (which only blocks
// package-EXTERNAL code) and Fixture's tag injection. A cross-package fixture
// cannot reference the unexported scope structs, so this fixture is
// irreducibly in-package.
//
// Each construction below is at NON-constructor scope (helper func body, never
// AST / Typed / Production / StandaloneModule / Fixture), so each of the five
// scope structs must be flagged — making every entry in runScopeStructNames a
// load-bearing per-member trip-wire. The functions are never invoked; they
// exist only as *ast.CompositeLit + *types.Info source for the detector.

package archtest

// runScopeConstructorBypassFixture constructs each of the five sealed RunScope
// structs OUTSIDE its sanctioned constructor — every line is a RED violation.
func runScopeConstructorBypassFixture() {
	_ = astRunScope{}        // bypass: astRunScope outside AST
	_ = typedRunScope{}      // bypass: typedRunScope outside Typed
	_ = productionRunScope{} // bypass: productionRunScope outside Production
	_ = dirRunScope{}        // bypass: dirRunScope outside StandaloneModule
	_ = fixtureRunScope{}    // bypass: fixtureRunScope outside Fixture
}

// runScopeConstructorGreenNegatives constructs NON-scope structs of package
// archtest outside any constructor — these MUST NOT be flagged. They are the
// GREEN false-positive control: the FixtureCoverage exact-count lock (== 5,
// the RED lines above) makes them load-bearing — if a non-scope name were
// mistakenly added to runScopeStructNames, one of these would trip and the
// count would exceed 5.
func runScopeConstructorGreenNegatives() {
	_ = TypedOpts{}   // not a RunScope struct → must NOT be flagged
	_ = FixtureOpts{} // not a RunScope struct → must NOT be flagged
	_ = Diagnostic{}  // not a RunScope struct → must NOT be flagged
}
