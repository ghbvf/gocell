//go:build archtest_fixture

// Package passfunnelfixture contains intentionally-violating archtest entry
// point usages that exercise the PASS-FUNNEL-* meta-archtest detectors in
// pass_funnel_test.go. Gated by the archtest_fixture build tag (kept as
// a literal here because Go's //go:build syntax cannot reference Go
// constants — must agree with the literal value of archtest.FixtureBuildTag
// declared in tools/archtest/fixture.go).
//
// TestPassFunnel_FixtureCoverage loads this package with the
// archtest_fixture tag via typeseval.SharedResolver (framework self-test
// exempt from PASS-FUNNEL-LOADPACKAGES-01) and asserts each rule
// detector emits ≥ 1 diagnostic. Removing or modifying any of the
// reference lines below turns one of the coverage assertions red — locking
// the rule pipeline at the live-AST level rather than the data-snapshot
// level (per AI-rebust charter "盲区自检").
//
// The fixture uses VALUE references (`_ = scanner.EachFile`) instead of
// call expressions. The PASS-FUNNEL detectors run typeseval.ResolvePackageRef
// over every SelectorExpr / bare Ident in the file, and ResolvePackageRef
// does NOT distinguish a function value reference from a call site (both
// produce *types.Func or *types.PkgName resolutions). Using value
// references lets the fixture stay free of testing import, *testing.T
// parameters, or scope.ModuleScope("") boilerplate that would obscure the
// detector contract.
//
// # Forms covered
//
// Each banned symbol is referenced in three import shapes so the detector
// is exercised across the AST forms typeseval.ResolvePackageRef resolves:
//
//   - qualified-import (`scannerpkg.EachFile` after named/regular import)
//   - alias-import     (`sn.EachFile` after `import sn "<path>"`)
//   - dot-import       (`EachFile` after `import . "<path>"` — bare Ident
//     scan; sister rule SCANNER-FRAMEWORK-USAGE-01's Path A.1+A.3 shape)
//
// Plus a direct packages-import violation for PASS-FUNNEL-PACKAGES-IMPORT-01.
//
// PASS-FUNNEL-RESOLVE-01 violations are added below for the 8 typeseval
// helpers and scanner.ImportBan, exercising the same three import forms.
package passfunnelfixture

import (
	// VIOLATION: PASS-FUNNEL-PACKAGES-IMPORT-01 (qualified import path scan).
	"golang.org/x/tools/go/packages"

	// VIOLATION sources for PASS-FUNNEL-EACHFILE-01 — qualified + alias + dot forms.
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	. "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	sn "github.com/ghbvf/gocell/tools/archtest/internal/scanner"

	// VIOLATION sources for PASS-FUNNEL-LOADPACKAGES-01 — qualified + alias + dot forms.
	// VIOLATION sources for PASS-FUNNEL-RESOLVE-01 — same package, different symbols.
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	te "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"

	// Cross-pkg exported const source for PASS-FUNNEL-FIXTURE-TAG-01 Form D
	// (`archtest.FixtureBuildTag`). No cycle: archtest package does not import
	// passfunnelfixture (it loads this fixture at runtime via SharedResolver),
	// so a child→parent import is safe.
	"github.com/ghbvf/gocell/tools/archtest"
)

// VIOLATION samples — value references suffice for typeseval.ResolvePackageRef
// to resolve the package+symbol pair (no call expression needed). Listed at
// file scope so the AST contains stable SelectorExpr / Ident nodes for the
// detector to walk. None of these are ever invoked at runtime; the file
// must merely type-check so packages.Load delivers full TypesInfo to the
// archtest pipeline.
var (
	// PASS-FUNNEL-EACHFILE-01 violations
	_ = scanner.EachFile // qualified
	_ = sn.EachFile      // alias-import
	_ = EachFile         // dot-import (bare Ident)

	// PASS-FUNNEL-LOADPACKAGES-01 violations
	_ = typeseval.LoadPackages           // qualified
	_ = typeseval.SharedResolver         // qualified
	_ = typeseval.LoadProductionPackages // qualified (Stage 1.7 funnel widen)
	_ = typeseval.EachFileInPackage      // qualified (#522 review A1, ADR §(c))
	_ = te.LoadPackages                  // alias-import
	_ = te.SharedResolver                // alias-import
	_ = te.LoadProductionPackages        // alias-import (Stage 1.7 funnel widen)
	_ = te.EachFileInPackage             // alias-import (#522 review A1, ADR §(c))

	// Force a packages reference so the import is not elided. The Config
	// type usage exists only to keep the import live; the import itself is
	// the PASS-FUNNEL-PACKAGES-IMPORT-01 violation (path scan, not symbol).
	_ packages.Config

	// ── PASS-FUNNEL-RESOLVE-01 violations ─────────────────────────────────
	// typeseval helper symbols banned from business *_test.go (8 symbols).
	// Three import forms each (qualified / alias / dot-import where applicable).

	// ResolvePackageRef
	_ = typeseval.ResolvePackageRef // qualified
	_ = te.ResolvePackageRef        // alias-import
	// dot-import form: ResolvePackageRef is a *types.Func in typeseval pkg,
	// but the dot import is already declared above (`. "…/typeseval"` is not
	// valid Go — only one dot-import per package path per file). The dot-import
	// of typeseval would conflict with the qualified import above.
	// The dot-import shape is exercised via scanner.ImportBan below (dot-import
	// of scanner is already present via `. "…/scanner"`).

	// ResolveMethodCall
	_ = typeseval.ResolveMethodCall // qualified
	_ = te.ResolveMethodCall        // alias-import

	// EvaluateConstString
	_ = typeseval.EvaluateConstString // qualified
	_ = te.EvaluateConstString        // alias-import

	// FlatNonDefaultTags
	_ = typeseval.FlatNonDefaultTags // qualified
	_ = te.FlatNonDefaultTags        // alias-import

	// KnownNonDefaultTags
	_ = typeseval.KnownNonDefaultTags // qualified
	_ = te.KnownNonDefaultTags        // alias-import

	// ParseBuildConstraint
	_ = typeseval.ParseBuildConstraint // qualified
	_ = te.ParseBuildConstraint        // alias-import

	// IsGeneratedRelPath
	_ = typeseval.IsGeneratedRelPath // qualified
	_ = te.IsGeneratedRelPath        // alias-import

	// BuildContextPredicate
	_ = typeseval.BuildContextPredicate // qualified
	_ = te.BuildContextPredicate        // alias-import

	// scanner.ImportBan — qualified and alias forms.
	// The dot-import form is: `_ = ImportBan` (bare Ident after `. "…/scanner"`).
	_ = scanner.ImportBan{} // qualified (value reference, zero-value struct literal)
	_ = sn.ImportBan{}      // alias-import
	_ = ImportBan{}         // dot-import (bare Ident from `. "…/scanner"` above)
)

// PASS-FUNNEL-FIXTURE-TAG-01 V' RED — type-aware (callee, arg) form-uniqueness.
// localFixtureTag exercises Form B: same-package const Ident, which
// EvaluateConstString resolves to the literal value "archtest_fixture".
const localFixtureTag = "archtest_fixture"

// fixtureTagBypassRedForms exercises all 4 EvaluateConstString-resolvable
// arg shapes that a business archtest could in principle use to feed the
// archtest_fixture build tag to a loader from LOADER_SET (typeseval.
// SharedResolver / LoadPackages / LoadProductionPackages plus archtest.
// RunTyped / RunTypedProduction / RunTypedDir). The detector must catch
// every form regardless of whether the literal is direct, via local const,
// via cross-pkg exported const, or via const concatenation.
//
// The function is never invoked at runtime; the package is gated by
// //go:build archtest_fixture and exists only as *ast.CallExpr +
// *types.Info source for analysis. typeseval.SharedResolver is chosen as
// the LOADER_SET canary because (i) it has the simplest signature for
// fixture construction (positional tags slice at arg 3), (ii) it is
// already a permanent RunTypedFixture-adjacent loader that business
// archtest must not call directly (also caught by PASS-FUNNEL-LOADPACKAGES-01),
// and (iii) the detector predicate is callee-shape-agnostic across the
// LOADER_SET — a single callee suffices to lock all 4 arg shapes; the
// remaining LOADER_SET members add no new arg-shape coverage axis.
func fixtureTagBypassRedForms() {
	// Form A — BasicLit STRING literal direct.
	_, _ = typeseval.SharedResolver("/dummy", false, []string{"archtest_fixture"}, "x")
	// Form B — same-pkg const Ident (localFixtureTag above).
	_, _ = typeseval.SharedResolver("/dummy", false, []string{localFixtureTag}, "x")
	// Form C — BinaryExpr const concatenation.
	_, _ = typeseval.SharedResolver("/dummy", false, []string{"archtest" + "_fixture"}, "x")
	// Form D — cross-pkg SelectorExpr to exported const (archtest.FixtureBuildTag).
	_, _ = typeseval.SharedResolver("/dummy", false, []string{archtest.FixtureBuildTag}, "x")
}
