//go:build archtest_fixture

// Package passfunnelfixture contains intentionally-violating archtest entry
// point usages that exercise the PASS-FUNNEL-* meta-archtest detectors in
// pass_funnel_test.go. Gated by the archtest_fixture build tag (kept as
// a literal here because Go's //go:build syntax cannot reference Go
// constants — must agree with the literal value of the unexported
// fixtureBuildTag const declared in tools/archtest/fixture.go).
//
// TestPassFunnel_FixtureCoverage loads this package with the
// archtest_fixture tag via typeseval.SharedResolver (framework self-test
// exempt from PASS-FUNNEL-LOADPACKAGES-01) and asserts each rule
// detector emits ≥ 1 diagnostic. Removing or modifying any of the
// reference lines below turns one of the coverage assertions red — locking
// the rule pipeline at the live-AST level rather than the data-snapshot
// level (per AI-robust charter "盲区自检").
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
// PASS-FUNNEL-RESOLVE-01 violations are added below for the 9 typeseval
// helpers and scanner.ImportBan, exercising the same three import forms.
package passfunnelfixture

import (
	// VIOLATION: PASS-FUNNEL-PACKAGES-IMPORT-01 (qualified import path scan).
	"golang.org/x/tools/go/packages"

	// VIOLATION sources for PASS-FUNNEL-FIXTURE-TAG-01 Form G — new typed-scope
	// constructors (Typed / Production / StandaloneModule) added to fixtureTagLoaderSet
	// by issue #1037 §1d after RunTyped/RunTypedProduction/RunTypedDir were deleted.
	archtest "github.com/ghbvf/gocell/tools/archtest"

	// VIOLATION sources for PASS-FUNNEL-EACHFILE-01 — qualified + alias + dot forms.
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	. "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	sn "github.com/ghbvf/gocell/tools/archtest/internal/scanner"

	// VIOLATION sources for PASS-FUNNEL-LOADPACKAGES-01 — qualified + alias + dot forms.
	// VIOLATION sources for PASS-FUNNEL-RESOLVE-01 — same package, different symbols.
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	te "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"

	// VIOLATION sources for PASS-FUNNEL-RESOLVE-01 — callresolver helpers
	// (qualified + alias + dot-import; unlike typeseval, callresolver exports no
	// name that collides with the scanner dot-import, so all 3 forms coexist —
	// the dot-import form locks resolveBarePkgSymbol's *types.Func + *types.TypeName
	// branches for callresolver directly).
	"github.com/ghbvf/gocell/tools/archtest/internal/callresolver"
	. "github.com/ghbvf/gocell/tools/archtest/internal/callresolver"
	cr "github.com/ghbvf/gocell/tools/archtest/internal/callresolver"
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
	_ = typeseval.LoadPackages            // qualified
	_ = typeseval.SharedResolver          // qualified
	_ = typeseval.SharedWorkspaceResolver // qualified
	_ = typeseval.LoadProductionPackages  // qualified (Stage 1.7 funnel widen)
	_ = typeseval.EachFileInPackage       // qualified (#522 review A1, ADR §(c))
	_ = te.LoadPackages                   // alias-import
	_ = te.SharedResolver                 // alias-import
	_ = te.SharedWorkspaceResolver        // alias-import
	_ = te.LoadProductionPackages         // alias-import (Stage 1.7 funnel widen)
	_ = te.EachFileInPackage              // alias-import (#522 review A1, ADR §(c))

	// Force a packages reference so the import is not elided. The Config
	// type usage exists only to keep the import live; the import itself is
	// the PASS-FUNNEL-PACKAGES-IMPORT-01 violation (path scan, not symbol).
	_ packages.Config

	// ── PASS-FUNNEL-RESOLVE-01 violations ─────────────────────────────────
	// typeseval helper symbols banned from business *_test.go (9 symbols).
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

	// ResolveEnclosingFunc
	_ = typeseval.ResolveEnclosingFunc // qualified
	_ = te.ResolveEnclosingFunc        // alias-import

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

	// callresolver helpers (qualified + alias). FuncDeclContext{} exercises the
	// *types.TypeName struct-literal branch (same shape as scanner.ImportBan{}).
	_ = callresolver.WalkFuncDecls     // qualified
	_ = callresolver.IsCallToPkgFunc   // qualified
	_ = callresolver.HasReceiver       // qualified
	_ = callresolver.FuncDeclContext{} // qualified (TypeName struct-literal ref)
	_ = cr.WalkFuncDecls               // alias-import
	_ = cr.IsCallToPkgFunc             // alias-import
	_ = cr.HasReceiver                 // alias-import
	_ = cr.FuncDeclContext{}           // alias-import
	// dot-import (bare Ident) — resolveBarePkgSymbol *types.Func (funcs) +
	// *types.TypeName (FuncDeclContext) branches for callresolver.
	_ = WalkFuncDecls     // dot-import
	_ = IsCallToPkgFunc   // dot-import
	_ = HasReceiver       // dot-import
	_ = FuncDeclContext{} // dot-import (TypeName bare Ident)
)

// PASS-FUNNEL-FIXTURE-TAG-01 V' RED — type-aware (callee, arg) form-uniqueness.
// localFixtureTag exercises Form B: same-package const Ident, which
// EvaluateConstString resolves to the literal value "archtest_fixture".
const localFixtureTag = "archtest_fixture"

// fixtureTagBypassRedForms exercises the const-resolvable arg shapes a business
// archtest could use to feed the archtest_fixture build tag to a loader from
// LOADER_SET (typeseval.SharedResolver / LoadPackages / LoadProductionPackages
// plus archtest.Typed / Production / StandaloneModule / runTypedWithRoot).
// The detector must catch every form regardless of whether the literal is
// direct, via local const, or via const concatenation.
//
// Note (#944): the former Form D (cross-pkg SelectorExpr archtest.FixtureBuildTag)
// is GONE — fixtureBuildTag is now unexported, so the cross-package selector is a
// compile error, not an archtest finding. That vector is type-system-Hard; no RED
// fixture can (or should) express it. The detector's SelectorExpr walker is
// retained as defense-in-depth against any future re-exported const equal to the
// sentinel, but has no live RED fixture by design.
//
// The function is never invoked at runtime; the package is gated by
// //go:build archtest_fixture and exists only as *ast.CallExpr +
// *types.Info source for analysis.
//
// Two coverage axes coexist:
//   - Arg-shape axis (Forms A / B / C): typeseval.SharedResolver is the canary
//     because (i) it has the simplest signature (positional tags slice at arg 3),
//     (ii) it is a loader business archtest must not call directly, and (iii) the
//     detector predicate is callee-shape-agnostic across the LOADER_SET, so a
//     single callee suffices to lock the const-resolvable arg shapes.
//   - Per-member callee axis (Forms G / H / I): each exported archtest typed-scope
//     constructor in fixtureTagLoaderSet (Typed / Production / StandaloneModule)
//     gets its own trip-wire so DROPPING it from the set fails CI — a per-member
//     regression lock, not a new arg-shape axis. (runTypedWithRoot is Form E,
//     in-package; the typeseval loaders LoadPackages / LoadProductionPackages are
//     also covered by PASS-FUNNEL-LOADPACKAGES-01's per-symbol lock.)
func fixtureTagBypassRedForms() {
	// Form A — BasicLit STRING literal direct.
	_, _ = typeseval.SharedResolver("/dummy", false, []string{"archtest_fixture"}, "x")
	// Form B — same-pkg const Ident (localFixtureTag above).
	_, _ = typeseval.SharedResolver("/dummy", false, []string{localFixtureTag}, "x")
	// Form C — BinaryExpr const concatenation.
	_, _ = typeseval.SharedResolver("/dummy", false, []string{"archtest" + "_fixture"}, "x")
	// Form G — BasicLit "archtest_fixture" via archtest.Typed (per-member callee).
	_ = archtest.Typed(archtest.TypedOpts{Tags: []string{"archtest_fixture"}}, nil)
	// Form H — BasicLit "archtest_fixture" via archtest.Production (per-member callee).
	_ = archtest.Production(archtest.TypedOpts{Tags: []string{"archtest_fixture"}})
	// Form I — BasicLit "archtest_fixture" via archtest.StandaloneModule (per-member callee).
	_ = archtest.StandaloneModule("/dummy", archtest.TypedOpts{Tags: []string{"archtest_fixture"}}, nil)
}

// fixtureTagSlice exercises Form F: a same-file var bound to a []string
// composite literal carrying a fixture-tag-resolving element (localFixtureTag).
// At the loader call site the arg is a plain *ast.Ident (the var name), which
// EvaluateConstString does NOT resolve — the detector must trace the binding
// (collectFixtureTagBoundObjects) to catch this var-indirection form. See
// pass_funnel_test.go diagsFixtureTagBypass Blind-spot closure for #944.
var fixtureTagSlice = []string{localFixtureTag}

// nonFixtureTagSlice is the GREEN-parity negative for Form F: a var bound to a
// non-fixture tag. Feeding it to a loader must NOT trip the detector — the
// bound-object collector only records slices whose elements EvaluateConstString
// to "archtest_fixture".
var nonFixtureTagSlice = []string{"integration"}

// containsTagStub is a non-LOADER_SET callee used by the GREEN-parity negative
// below: passing the fixture tag to a function that is NOT a loader must NOT
// trip the detector (the (callee, arg) pair disambiguates legitimate identity
// use from bypass).
func containsTagStub(tag string) bool { return tag == localFixtureTag }

// fixtureTagBypassVarFormAndParity exercises the var-binding bypass (Form F)
// plus the two GREEN-parity negatives that must produce ZERO diagnostics.
// Never invoked; *ast.CallExpr + *types.Info source only.
func fixtureTagBypassVarFormAndParity() {
	// Form F — same-file var bound to a fixture-tag slice fed to a loader.
	_, _ = typeseval.SharedResolver("/dummy", false, fixtureTagSlice, "x")
	// GREEN-parity 1 — non-fixture tag var fed to a loader: MUST NOT trip.
	_, _ = typeseval.SharedResolver("/dummy", false, nonFixtureTagSlice, "x")
	// GREEN-parity 2 — fixture tag fed to a non-LOADER_SET callee: MUST NOT trip.
	_ = containsTagStub(localFixtureTag)
}
