package archtest

import "testing"

// FixtureOpts is the option struct accepted by RunTypedFixture.
// It deliberately lacks a Tags field — the "archtest_fixture" build tag
// is supplied exclusively by RunTypedFixture's function body. Business
// callers therefore cannot express "load a fixture with a custom tag" at
// the type level; passing Tags would require dropping back to RunTyped.
//
// This is the Hard-form upgrade of typed function choice: not only the
// function name (RunTyped vs RunTypedFixture) but the input struct field
// set (FixtureOpts has no Tags) participates in the type-system constraint.
// See AI-rebust §Hard 范本 in .claude/rules/gocell/ai-collab.md.
type FixtureOpts struct {
	Tests bool
}

// RunTypedFixture loads packages tagged with the conventional
// archtest_fixture build tag, then dispatches via Pass + Rule. It is the
// typed funnel for fixture-package archtest loading; all 6 existing
// fixture-load sites in pass_test.go and pass_funnel_test.go's
// TestPassFunnel_FixtureCoverage MUST use this entry point (the
// FixtureCoverage test uses typeseval.SharedResolver directly for its
// own framework-internal reasons documented in pass_funnel_test.go).
//
// The "archtest_fixture" literal is the single source — see passfunnelfixture
// and basesliceredfixture sub-packages' //go:build directives, which must
// agree with this literal (Go's build directive syntax cannot reference a
// Go constant; this is the structural reason there is no FixtureBuildTag
// const).
//
// Parameter type *testing.T (not testing.TB): fixture loading has no
// spy fatal-path requirement. RunTyped / RunTypedProduction also use
// *testing.T. RunTypedDir uses testing.TB for its standalone-fixture-module
// spy testing — orthogonal use case. See ADR 202605141519 §Migration path
// Stage 4.
func RunTypedFixture(t *testing.T, opts FixtureOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	return runTypedWithRoot(t, findModuleRoot(t), TypedOpts{
		Tests: opts.Tests,
		Tags:  []string{"archtest_fixture"},
	}, patterns, rule)
}
