package archtest

// INVARIANT: YAML-QUOTE-FUNNEL-01
//
// yaml_quote_funnel_test.go — test entry points for YAML-QUOTE-FUNNEL-01.
//
// Rule logic, path consts, and helper funcs live in
// yaml_quote_funnel_invariants.go (non-test, importable). This file dogfoods
// CheckYAMLQuoteFunnel against GoCell itself and holds the fixture-based
// reverse self-tests that share scanYAMLQuoteFunnel — single source, no
// parallel rule body.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestYAMLQuoteFunnel enforces YAML-QUOTE-FUNNEL-01 on the production
// codebase: every yamlsafe.Scalar(...) type conversion outside pkg/yamlsafe
// must have a yamlsafe.Quote(...) call (or a typed Scalar value) as its
// argument.
func TestYAMLQuoteFunnel(t *testing.T) {
	t.Parallel()
	Report(t, yamlQuoteFunnelRule, CheckYAMLQuoteFunnel(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestYAMLQuoteFunnel_DetectsViolation is the reverse self-test: feed the
// scanner the pkg/yamlsafe production AST (which intentionally constructs
// Scalar from raw strings inside Quote()) and assert the scanner produces
// at least one violation. The outer TestYAMLQuoteFunnel skips pkg/yamlsafe
// by path, so production stays clean; this test invokes the scanner with the
// path filter bypassed to exercise detection.
//
// The count threshold is ≥1 (not ≥3) so that internal refactoring of
// Quote()'s implementation does not break this guard. The invariant is that
// detection fires at least once — proving types.Info resolution works.
//
// If the scanner ever stops detecting bare conversions — e.g. the
// types.Info-based callee resolution silently fails — this test goes red
// and the YAML-QUOTE-FUNNEL-01 Hard property has regressed.
func TestYAMLQuoteFunnel_DetectsViolation(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic
	found := false

	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./pkg/yamlsafe/"}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != yamlsafePkgPath {
				return nil
			}
			found = true
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return nil
		})

	require.True(t, found, "yamlsafe package must be loaded")
	require.GreaterOrEqual(t, len(diags), 1,
		"scanner must detect at least one bare Scalar(raw) conversion inside yamlsafe.go; "+
			"empty result means the types.Info-based detection silently regressed")
}

// TestYAMLQuoteFunnel_DetectsAliasBypass loads the archtest_fixture-gated
// yamlquotefixture package (which declares `type AliasOfScalar = yamlsafe.Scalar`)
// and asserts the scanner detects the bare alias conversion site (BypassViaAlias)
// while leaving the Quote-wrapped compliant site (CompliantQuoted) untouched.
//
// Without types.Unalias resolution in isYAMLScalarConversion, the TypeName for
// AliasOfScalar resolves to the caller package rather than pkg/yamlsafe, and the
// check silently passes — making this test fail RED and proving the regression.
func TestYAMLQuoteFunnel_DetectsAliasBypass(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic
	found := false

	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/yamlquotefixture/"}),

		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != yamlquotefixturePkgPath {
				return nil
			}
			found = true
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return nil
		})

	require.True(t, found, "yamlquotefixture package must be loaded (check -tags=archtest_fixture)")
	// The fixture has exactly 3 bypass sites (BypassViaAlias / BypassViaLiteral /
	// BypassViaConstConcat) and 2 compliant controls (CompliantQuoted /
	// CompliantTypedScalar) that must NOT fire. Asserting == 3 (not >= 1) makes
	// this test red on BOTH directions: if types.Unalias resolution regresses,
	// BypassViaAlias drops out (count → 2); if a compliant control starts firing,
	// count → 4. diags[0] is the alias site (AST top-down: it is first in
	// fixture.go), so the message assertion anchors alias detection specifically.
	require.Equal(t, 3, len(diags),
		"expected exactly the 3 bypass sites; alias-detection regression or a "+
			"compliant control firing would shift the count. got: %v", diags)
	require.Contains(t, diags[0].Message, "yamlsafe.Scalar(...)")
}

// TestYAMLQuoteFunnel_DetectsLiteralBypass loads the archtest_fixture-gated
// yamlquotefixture package and asserts that string-literal + const-concat
// conversion sites (BypassViaLiteral / BypassViaConstConcat) are detected
// by the const-value branch of allowedScalarConversionArg. Without
// info.Types[arg].Value-based filtering, Go's contextual typing would
// silently route them through the named-type "already Scalar" branch.
//
// CompliantTypedScalar (identity conversion through a typed local variable)
// must NOT fire — it exercises the allowed path where info.Types[arg].Value
// is nil and the named-type check sees yamlsafe.Scalar.
func TestYAMLQuoteFunnel_DetectsLiteralBypass(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic
	found := false

	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/yamlquotefixture/"}),

		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != yamlquotefixturePkgPath {
				return nil
			}
			found = true
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return nil
		})

	require.True(t, found, "yamlquotefixture package must be loaded (check -tags=archtest_fixture)")
	// Expect violations from exactly: BypassViaAlias, BypassViaLiteral,
	// BypassViaConstConcat. CompliantQuoted and CompliantTypedScalar must NOT
	// fire. == 3 (not >= 3) closes the over-detection gap: if allowedScalarConversionArg
	// regresses to flag a compliant control, the count climbs to 4 and this test
	// goes red instead of silently passing.
	require.Equal(t, 3, len(diags),
		"scanner must detect exactly the alias + literal + concat bypass sites "+
			"(compliant controls must not fire); got %d: %v", len(diags), diags)
}
