package archtest

import "testing"

// FixtureBuildTag is the build-tag literal that gates archtest fixture
// sub-packages (internal/passfunnelfixture, internal/basesliceredfixture,
// internal/rawparamfixture, internal/auditledgerfixture,
// internal/inspectorredfixture, internal/wrapfixture/*, internal/
// authroutemutexfixture, internal/refreshinvariantsfixture, internal/
// sessionprotocolfixture, internal/transientmarkerfixture,
// internal/yamlquotefixture). Business archtest code that needs to identify
// this build tag at the Go-code level (e.g., panic_invariants_test.go
// skipping the fixture tag group inside a module-wide RunTyped scan) MUST
// reference this const rather than the bare "archtest_fixture" literal —
// PASS-FUNNEL-FIXTURE-TAG-01 archtest enforces that requirement by
// rejecting BasicLit STRING "archtest_fixture" in business *_test.go files.
//
// fixture sub-packages' //go:build archtest_fixture directives must
// hard-code the literal because Go build-directive syntax cannot reference
// a Go constant; this const is the parallel single source for Go-code
// paths. Both sources of truth point at the same value by construction
// (RunTypedFixture body below uses FixtureBuildTag, and the build directive
// in each fixture package is a verbatim "archtest_fixture" string).
const FixtureBuildTag = "archtest_fixture"

// FixtureOpts is the option struct accepted by RunTypedFixture.
// It deliberately lacks a Tags field — the archtest_fixture build tag is
// supplied exclusively by RunTypedFixture's function body. Business
// callers therefore cannot express "load a fixture with a custom tag" at
// the type level; passing Tags would require dropping back to RunTyped,
// which is then caught upstream by PASS-FUNNEL-FIXTURE-TAG-01 (façade
// bypass closure).
//
// This is the Hard-form upgrade of typed function choice: not only the
// function name (RunTyped vs RunTypedFixture) but the input struct field
// set (FixtureOpts has no Tags) participates in the type-system constraint.
// See AI-rebust §Hard 范本 in .claude/rules/gocell/ai-collab.md.
type FixtureOpts struct {
	Tests bool
}

// RunTypedFixture loads packages tagged with the archtest_fixture build
// tag, then dispatches via Pass + Rule. It is the typed funnel for
// fixture-package archtest loading; all fixture-load sites across archtest
// *_test.go MUST use this entry point. The framework-internal exceptions
// are limited to files in passFunnelPermanentExempt (pass_funnel_test.go,
// pass_test.go, archtest_test.go in tools/archtest/), which call
// typeseval.SharedResolver directly because they implement or directly
// test the funnel machinery and cannot be expressed through Pass without
// circular dependency — see pass_funnel_test.go's passFunnelPermanentExempt
// godoc for the structural justification.
//
// Parameter type *testing.T (not testing.TB): fixture loading has no
// spy fatal-path requirement. RunTyped / RunTypedProduction also use
// *testing.T. RunTypedDir uses testing.TB for its standalone-fixture-module
// spy testing — orthogonal use case. See ADR 202605141519 §Migration path
// Stage 4.
//
// AI-rebust (funnel double-lock):
//   - Outward Hard (business callers, downstream funnel side): FixtureOpts
//     has no Tags field; writing RunTypedFixture(t, FixtureOpts{Tags: ...},
//     ...) is a compile error.
//   - Upstream Hard (façade bypass closure): PASS-FUNNEL-FIXTURE-TAG-01
//     archtest rejects any BasicLit STRING "archtest_fixture" in business
//     *_test.go files (archtest-bound form-uniqueness — see
//     pass_funnel_test.go diagsFixtureTagBypass godoc for evidence).
//   - Inward Medium (framework internal): the field set of FixtureOpts itself
//     is frozen by TestRunTypedFixture_FixtureOptsLacksTagsField via reflect
//     assertion (NumField == 1, sole field "Tests" of kind Bool) — drift here
//     is a test failure, not a compile error.
func RunTypedFixture(t *testing.T, opts FixtureOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	return runTypedWithRoot(t, findModuleRoot(t), TypedOpts{
		Tests: opts.Tests,
		Tags:  []string{FixtureBuildTag},
	}, patterns, rule)
}
