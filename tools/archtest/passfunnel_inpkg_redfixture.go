//go:build archtest_fixture

// This file is an in-package RED fixture for PASS-FUNNEL-FIXTURE-TAG-01,
// gated by the archtest_fixture build tag so it is invisible to the default
// `go build` / `go test` / live archtest scan (which loads tags=nil). It is
// loaded only by TestPassFunnel_FixtureCoverage via a dedicated
// SharedResolver(root, false, []string{"archtest_fixture"}, "./tools/archtest")
// call.
//
// # Why this fixture must live in package archtest
//
// The same-package bypass vector closed by #944 is a business *_test.go in
// package archtest calling the UNEXPORTED runTypedWithRoot directly with the
// fixture build tag — sidestepping both the FixtureOpts-has-no-Tags compile
// lock (it constructs TypedOpts, not FixtureOpts) and (pre-#944) the
// fixtureTagLoaderSet detector (runTypedWithRoot was not enumerated). A
// cross-package fixture (internal/passfunnelfixture) CANNOT reference the
// unexported runTypedWithRoot, so Form E is irreducibly in-package.
//
// The function is never invoked; it exists only as *ast.CallExpr +
// *types.Info source for the detector. typeseval.ResolvePackageRef resolves
// the bare-Ident callee `runTypedWithRoot` to (archtestPkgPath,
// "runTypedWithRoot"), which the loader set matches; the FixtureBuildTag
// Ident inside the Tags slice EvaluateConstString-resolves to
// "archtest_fixture".

package archtest

// fixtureTagBypassInPkgRunTypedWithRoot exercises Form E: the same-package
// unexported runTypedWithRoot loader fed the fixture build tag.
func fixtureTagBypassInPkgRunTypedWithRoot() {
	// Form E — same-package unexported runTypedWithRoot loader.
	_ = runTypedWithRoot(nil, "", TypedOpts{Tags: []string{FixtureBuildTag}}, nil, nil)
}
