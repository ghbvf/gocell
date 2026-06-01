package archtest

// fixtureBuildTag is the build-tag literal that gates archtest fixture
// sub-packages. Three placement conventions coexist:
//
//   - internal/<name>/ — fixture exposed as a Go sub-package importable by
//     name; used when the fixture needs to be referenced from other
//     archtest code (e.g. internal/passfunnelfixture, internal/
//     basesliceredfixture, internal/rawparamfixture, internal/
//     auditledgerfixture, internal/inspectorredfixture, internal/
//     wrapfixture/*, internal/authroutemutexfixture, internal/
//     refreshinvariantsfixture, internal/sessionprotocolfixture,
//     internal/transientmarkerfixture, internal/yamlquotefixture).
//   - testdata/<rule_fixtures>/<case>/ — pattern-based fixture loaded by
//     path; used for rules with a golden file per case (e.g.
//     testdata/testwait_external_fixtures/, testdata/eventually_funnel_fixtures/).
//   - package-root non-_test.go — in-package fixture used ONLY when the
//     fixture must reference an UNEXPORTED package-archtest symbol that a
//     cross-package internal/ fixture cannot reach (e.g.
//     passfunnel_inpkg_redfixture.go references the unexported runTypedWithRoot
//     for PASS-FUNNEL-FIXTURE-TAG-01 Form E). Loaded via a dedicated
//     SharedResolver(..., "./tools/archtest") call in the rule's coverage test.
//
// All three conventions are protected by the archtest_fixture build tag and
// the same façade-bypass guard. Prefer internal/ or testdata/; reach for the
// package-root form only for the unexported-symbol case above.
//
// # Unexported by design (#944)
//
// This const is package-private. Business archtest code OUTSIDE package
// archtest cannot reference it. After #944 removed archtest_fixture from the
// generic tag union (typeseval.KnownNonDefaultTags / FlatNonDefaultTags), no
// module-wide scan loads fixture-tagged code, so business code no longer has
// any reason to identify the fixture tag at the Go level. Unexporting turns
// the former cross-package "typed-identity" reference (the old
// PASS-FUNNEL-FIXTURE-TAG-01 Form D, archtest.FixtureBuildTag fed to a loader)
// into a compile error — upgrading that bypass vector from
// archtest-detector-caught to type-system-unexpressible (Hard). See ADR
// 202605141519 §#944.
//
// fixture sub-packages' //go:build archtest_fixture directives must hard-code
// the literal because Go's //go:build syntax cannot reference a Go constant;
// this const is the parallel single source for Go-code paths within package
// archtest. Both sources of truth point at the same value by construction
// (RunTypedFixture body below uses fixtureBuildTag, and the build directive in
// each fixture package is a verbatim "archtest_fixture" string).
const fixtureBuildTag = "archtest_fixture"

// FixtureOpts is the option struct accepted by [Fixture].
// It deliberately lacks a Tags field — the archtest_fixture build tag is
// supplied exclusively by the [Run] dispatch for a fixtureRunScope. Business
// callers therefore cannot express "load a fixture with a custom tag" at the
// type level; passing Tags would require dropping back to Typed /
// runTypedWithRoot, which is then caught upstream by
// PASS-FUNNEL-FIXTURE-TAG-01 (façade bypass closure).
//
// This is the Hard-form upgrade of typed function choice: not only the
// function name (RunTyped vs RunTypedFixture) but the input struct field set
// (FixtureOpts has no Tags) participates in the type-system constraint. See
// AI-robust §Hard 范本 in .claude/rules/gocell/ai-robust.md.
type FixtureOpts struct {
	Tests bool
}

// fixtureRunScope dispatches [Run] in typed mode over fixture-tagged packages.
// The archtest_fixture build tag is NOT carried on this struct — it is injected
// by the [Run] dispatch for this case, so the tag never appears at a business
// call site.
type fixtureRunScope struct {
	opts     FixtureOpts
	patterns []string
}

func (fixtureRunScope) isRunScope() {}

// Fixture builds a typed [RunScope] over packages tagged with the
// archtest_fixture build tag. It is the typed funnel for fixture-package
// archtest loading; all fixture-load sites across archtest *_test.go MUST use
// Run(t, Fixture(...), rule). The framework-internal exceptions are limited to
// files in passFunnelPermanentExempt (pass_funnel_test.go, pass_test.go,
// archtest_test.go in tools/archtest/), which call typeseval.SharedResolver
// directly because they implement or directly test the funnel machinery and
// cannot be expressed through Pass without circular dependency — see
// pass_funnel_test.go's passFunnelPermanentExempt godoc for the structural
// justification.
//
// The archtest_fixture build tag is supplied exclusively by the [Run] dispatch
// for a fixtureRunScope (pass.go) — not by this constructor's caller. The
// unified Run accepts testing.TB; the fixture scope has no spy fatal-path
// requirement, so no caller distinction is needed. See ADR 202605141519
// §Migration path Stage 4.
//
// AI-robust (funnel double-lock):
//   - Outward Hard (business callers, downstream funnel side): FixtureOpts has
//     no Tags field, and the unified Run takes a sealed RunScope (no tag
//     parameter at all) — there is no public surface that carries a build tag.
//   - Cross-package Form D Hard (type system, #944): fixtureBuildTag is
//     unexported, so business code outside package archtest cannot feed it to
//     a loader — a compile error, not an archtest finding.
//   - Upstream Hard (façade bypass closure): PASS-FUNNEL-FIXTURE-TAG-01
//     archtest rejects any (callee, arg) pair where the callee resolves to a
//     loader (archtest.runTypedWithRoot, or typeseval.SharedResolver /
//     LoadPackages / LoadProductionPackages) AND any arg subtree contains an
//     Expr that EvaluateConstString resolves to "archtest_fixture", OR an
//     *ast.Ident bound (same file) to a slice literal carrying such an Expr.
//     This catches the raw literal / same-pkg const Ident / BinaryExpr
//     const-concat / same-file var-binding forms uniformly via the helper's
//     resolution lattice (archtest-bound (callee, arg) form-uniqueness —
//     isomorphic to charter §Hard 范本 第 2 条 panic(panicregister.Approved)
//     form; see pass_funnel_test.go diagsFixtureTagBypass godoc for full
//     evidence + accepted Blind spots).
//   - Inward Medium (framework internal): the field set of FixtureOpts itself
//     is frozen by TestRun_Fixture_FixtureOptsLacksTagsField via reflect
//     assertion (NumField == 1, sole field "Tests" of kind Bool) — drift here
//     is a test failure, not a compile error.
func Fixture(opts FixtureOpts, patterns []string) RunScope {
	return fixtureRunScope{opts: opts, patterns: patterns}
}
