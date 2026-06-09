package archtest

// pass_funnel_test.go — meta-archtest: enforce archtest.Pass funnel.
//
//   - INVARIANT: PASS-FUNNEL-EACHFILE-01
//   - INVARIANT: PASS-FUNNEL-LOADPACKAGES-01
//   - INVARIANT: PASS-FUNNEL-PACKAGES-IMPORT-01
//   - INVARIANT: PASS-FUNNEL-RESOLVE-01
//   - INVARIANT: PASS-FUNNEL-FIXTURE-TAG-01
//
// The first four rules forbid archtest tools/archtest/<file>_test.go from
// reaching the legacy entry points directly. Authors must use archtest.Run
// (AST-only) / archtest.Run(t, Typed(...)) (typed) /
// archtest.Run(t, Fixture(FixtureOpts{...}, patterns), rule) (fixture
// packages) / archtest.Run(t, Production(...), rule) (production-only) via
// the Pass-Driver paradigm, and must call the façade helper functions in
// archtest.ResolvePackageRef / ResolveMethodCall / EvaluateConstString /
// FlatNonDefaultTags / KnownNonDefaultTags / Pass.IsFileInScope /
// Pass.IsGenerated instead of importing internal/typeseval directly.
//
// Module-path-agnostic note (#1639 M3 PR-8b): this file's platform symbol-path
// consts derive from PlatformModulePath (cleared from module_path_funnel.baseline),
// but its rule body stays in _test.go — it is itself in passFunnelPermanentExempt
// and reaches the raw typeseval loaders the funnel bans for everyone else, so a
// move into a pass-funnel-unscanned .go would open an unenforced loader bypass
// (an AI-HARD regression). The full move to a .go Check* (after extending the
// Pass funnel to scan .go / a whole-module-graph mode) is deferred to #1705.
//
// PASS-FUNNEL-FIXTURE-TAG-01 closes the façade-bypass leg of the
// archtest_fixture funnel: Fixture's outward Hard (FixtureOpts has
// no Tags field) prevents typed expression of "load fixture with custom
// tag" in business call sites, but Typed / Production / StandaloneModule /
// typeseval.SharedResolver still accept arbitrary Tags (via TypedOpts.Tags),
// so a business archtest could in principle pass the archtest_fixture build
// tag (in any const-resolvable form) to one of those constructors and bypass
// Fixture. The rule rejects any
// (callee, arg) pair where the callee resolves via *types.Info to a member
// of fixtureTagLoaderSet AND any arg subtree contains an Expr that
// EvaluateConstString resolves to "archtest_fixture" — catching BasicLit
// literal / same-pkg const Ident / cross-pkg SelectorExpr / BinaryExpr
// const-concat uniformly. The (callee, arg) pair shape disambiguates
// legitimate same-package identity uses (e.g., a non-loader callee receiving
// the unexported fixtureBuildTag const) from bypass (loader callee + same
// value resolves). Isomorphic to charter §Hard 范本
// 第 2 条 panic(panicregister.Approved(reason, value)) form. See
// docs/architecture/202605141519-adr-archtest-pass-funnel.md.
//
// Stage 2/3 migration completed in PR #522; LegacyAllowlist was cleared to
// zero. Stage 4 (this PR) deletes the archtestmeta package entirely and
// collapses TestPassFunnelGuardListSync to a single equality assertion
// against passFunnelPermanentExempt (3-entry Medium funnel — mechanical sync
// via double-declaration in Go map + .golangci.yml negative globs, enforced
// by exact-equality assertion).

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	cmp "github.com/google/go-cmp/cmp"
	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const (
	scannerPkgPath        = PlatformModulePath + "/tools/archtest/internal/scanner"
	archtestPkgPath       = PlatformModulePath + "/tools/archtest"
	typesevalPkgPath      = PlatformModulePath + "/tools/archtest/internal/typeseval"
	callresolverPkgPath   = PlatformModulePath + "/tools/archtest/internal/callresolver"
	packagesPkgPath       = "golang.org/x/tools/go/packages"
	usage02FixturesRelDir = "tools/archtest/internal/usage02fixtures"
)

// passFunnelPermanentExempt names archtest framework files that import or
// call the banned entry points by structural necessity:
//
//   - pass_funnel_test.go: implements the PASS-FUNNEL meta-archtest, must
//     reference the forbidden symbols.
//   - pass_test.go: unit-tests archtest.Run / buildTypedPass /
//     newPackageRel / isPackageWithTestFiles; the last three accept or
//     construct *packages.Package fixtures by signature.
//   - archtest_test.go: archtest driver self-tests (LAYER-05..10 + PGQUERY-01)
//     that cannot be expressed through the *Pass funnel for three structural
//     reasons:
//     (a) depgraph.FromPackages([]*packages.Package) — the depgraph constructor
//     reads only structural fields (PkgPath / Imports on *packages.Package),
//     not Syntax or TypesInfo, so it does not trigger the INV-1 cross-load
//     pairing risk; the Pass funnel intentionally hides .Syntax to prevent
//     INV-1, but depgraph.FromPackages is safe without that guard.
//     (b) checkCellPublicAPIAdapterTypes (LAYER-10) accesses pkg.Syntax and
//     pkg.TypesInfo on the same *packages.Package (single-package pairing —
//     not cross-load INV-1), but Pass.Pkg is *types.Package which intentionally
//     hides .Syntax; this file cannot express that access pattern via *Pass.
//     (c) This file calls typeseval.SharedResolver directly to obtain the full
//     *packages.Package slice for depgraph and LAYER-10 — a usage axis that
//     differs from business archtest authors who scan rule violations; it is
//     the framework's own integration test, structurally equivalent to
//     pass_test.go's buildTypedPass input side.
//     Future upgrade ARCHTEST-LAYER10-PASS-MIGRATION-01: migrate (b)+(c)
//     to the Pass model when a typed-field accessor is available.
//
// These exemptions survive stage-4 cleanup. They are checked by both
// the production PASS-FUNNEL detectors (skip these files entirely) AND
// TestPassFunnelGuardListSync (exact-equality assertions: yaml-exempt ==
// passFunnelPermanentExempt AND packages-import == passFunnelPermanentExempt).
//
// AI-robust grade: Medium. The 3-entry set is structurally necessary (these
// files implement or directly test the funnel machinery; type system cannot
// distinguish rule implementation from rule violator). Mechanical sync is
// enforced via double-declaration: Go map literal here AND matching
// "!**/tools/archtest/<file>" negative globs in .golangci.yml. Drift between
// the two declarations is detected by TestPassFunnelGuardListSync's exact
// equality assertions (failing CI on any single-sided addition or removal).
// Upgrading to Hard is infeasible without creating a new package boundary
// (archtestself) that adds complexity without security gain — see ADR
// docs/architecture/202605141519-adr-archtest-pass-funnel.md
// §passFunnelPermanentExempt.
var passFunnelPermanentExempt = map[string]bool{
	"tools/archtest/pass_funnel_test.go": true,
	"tools/archtest/pass_test.go":        true,
	"tools/archtest/archtest_test.go":    true,
}

// passFunnelTarget pairs a Pass-eligible scan target (file + rel-path + the
// package fset+info it belongs to) for one of the three rules to consume.
type passFunnelTarget struct {
	rel  string
	file *ast.File
	pkg  *packages.Package
}

// loadPassFunnelTargets resolves the archtest tree once via SharedResolver
// (shared with scanner_framework_usage_test.go's same load), filters to
// tools/archtest/<file>_test.go direct children, and applies the permanent
// self-exemption set. Stage 4 removed the LegacyAllowlist filter; all
// migration-period scaffold entries were cleared in PR #522 and the
// archtestmeta package deleted in Stage 4. The returned slice is what the
// three rules consume.
func loadPassFunnelTargets(t *testing.T) []passFunnelTarget {
	t.Helper()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var targets []passFunnelTarget
	seen := make(map[string]bool)
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if passFunnelPermanentExempt[rel] {
				continue
			}
			// Dedup across the regular + ".test" synthetic packages
			// that typeseval test-mode load returns for the same path.
			if seen[rel] {
				continue
			}
			seen[rel] = true
			targets = append(targets, passFunnelTarget{rel: rel, file: file, pkg: pkg})
		}
	}
	return targets
}

// diagsEachFile is the pure detector for PASS-FUNNEL-EACHFILE-01. Exposed
// separately from the Test* function so the fixture-coverage assertion
// (TestPassFunnel_FixtureCoverage) can dogfood the same logic against a
// build-tag-gated red fixture.
func diagsEachFile(tgt passFunnelTarget) []scanner.Diagnostic {
	return scanForForbiddenCallees(
		tgt,
		map[string]map[string]bool{scannerPkgPath: {"EachFile": true}},
		"archtest.Run (AST-only) / archtest.Run(t, Typed(...), rule) (typed)",
	)
}

// diagsLoadPackages is the pure detector for PASS-FUNNEL-LOADPACKAGES-01.
// It bans business archtest *_test.go files from directly calling typeseval
// package-load symbols: LoadPackages, SharedResolver, SharedWorkspaceResolver,
// LoadProductionPackages (Stage 1.7 funnel widen), and EachFileInPackage (the
// INV-1 suppression helper that the Pass funnel replaces — #522 review A1,
// closes ADR termination-criteria §(c)). Detection is type-aware via
// typeseval.ResolvePackageRef on all SelectorExpr / bare Ident nodes.
//
// # AI-robust: Medium
//
// Detection is type-aware (typeseval.ResolvePackageRef via *types.Info) and
// covers all three import forms (qualified, alias, dot-import). Not Hard
// because Go allows arbitrary aliasing; the detector requires *types.Info
// resolve rather than string-matching, so it cannot be bypassed by renaming
// an import alias.
//
// # Blind spots (per ai-robust.md Medium evidence requirement)
//
//   - File-scope var escape: `var loader = typeseval.LoadProductionPackages`
//     at file scope — the assignment SelectorExpr IS detected (trips the rule),
//     but a subsequent call via the variable from a different function is not.
//     This is the same Soft escape acknowledged in diagsResolveHelpers; accepted
//     because the initial value reference trips the rule.
//   - Cross-func Ident escape: an Ident whose name is bound to a loader in one
//     function and called from another is not detected without inter-procedural
//     analysis. No such pattern exists in production archtest today.
//
// # Per-form fixture coverage
//
// LoadPackages, SharedResolver, SharedWorkspaceResolver, LoadProductionPackages,
// and EachFileInPackage are each fixtured in two qualified + alias forms in redfixture.go.
// (LoadProductionPackages: Stage 1.7 addition; EachFileInPackage: #522 review A1.)
// Dot-import of typeseval is infeasible in redfixture.go (conflicting imports).
// TestPassFunnel_FixtureCoverage enforces ≥1 diagnostic per symbol, so
// removing any of the three loader fixture lines fails the coverage lock.
//
// Note on productionLoaderFunnelAllowlist: the loader-anchor test
// (TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3 in
// production_loader_funnel_test.go) is allowlisted because it calls
// SharedResolver with "./..." — not LoadProductionPackages — to prove that
// SharedResolver loads generated/ packages. LoadProductionPackages therefore
// needs no allowlist entry in productionLoaderFunnelAllowlist even though it
// is in the same diagsLoadPackages banned symbol set: business *_test.go code
// is banned from calling it directly, but the anchor test never does.
//
// # Known blind spot — non-test .go files
//
// PASS-FUNNEL-LOADPACKAGES-01 scope filters by HasSuffix("_test.go"), so
// non-test .go files in tools/archtest/ can directly call typeseval.
// {LoadPackages, SharedResolver, SharedWorkspaceResolver, LoadProductionPackages, EachFileInPackage}
// without tripping the rule. This is currently exempted by design for the
// TestMain warm-up bridge: tools/archtest/warm.go calls LoadProductionPackages
// from a non-test .go file to keep testmain_test.go free of typeseval imports
// (which would trip the rule).
//
// The blind spot is bounded by the structural property that non-test .go files
// in package archtest are framework-internal code — they cannot be reached by
// business archtest *_test.go callers, so the typeseval call set still flows
// through the PASS-FUNNEL contract from the user-facing surface.
//
// Future upgrade PASS-FUNNEL-NONTESTGO-EXEMPT-UPGRADE-01: extend the
// scope filter to scan non-test .go files with a warm.go allowlist, or
// seal warm.go behind a stronger typed boundary.
func diagsLoadPackages(tgt passFunnelTarget) []scanner.Diagnostic {
	return scanForForbiddenCallees(
		tgt,
		map[string]map[string]bool{
			typesevalPkgPath: {
				"LoadPackages":            true,
				"SharedResolver":          true,
				"SharedWorkspaceResolver": true,
				"LoadProductionPackages":  true,
				"EachFileInPackage":       true,
			},
		},
		"archtest.Run(t, Typed(...), rule) / archtest.Run(t, Production(...), rule)",
	)
}

// diagsPackagesImport is the pure detector for PASS-FUNNEL-PACKAGES-IMPORT-01.
func diagsPackagesImport(tgt passFunnelTarget) []scanner.Diagnostic {
	bannedQuoted := strconv.Quote(packagesPkgPath)
	var diags []scanner.Diagnostic
	for _, imp := range tgt.file.Imports {
		if imp == nil || imp.Path == nil {
			continue
		}
		if imp.Path.Value != bannedQuoted {
			continue
		}
		diags = append(diags, scanner.Diagnostic{
			Rel:  tgt.rel,
			Line: tgt.pkg.Fset.Position(imp.Pos()).Line,
			Message: fmt.Sprintf(
				"direct import of %q forbidden in archtest *_test.go; use archtest.Run with the "+
					"appropriate RunScope constructor — Typed (main module) / Production "+
					"(generated/-excluded) / Fixture (archtest_fixture packages) / StandaloneModule "+
					"(standalone fixture module) — e.g. archtest.Run(t, Typed(...), rule)",
				packagesPkgPath,
			),
		})
	}
	return diags
}

// diagsResolveHelpers is the pure detector for PASS-FUNNEL-RESOLVE-01.
// It bans business archtest *_test.go files from directly calling the 8
// typeseval helper symbols and scanner.ImportBan (as value refs or calls),
// covering qualified / alias / dot-import forms via typeseval.ResolvePackageRef.
//
// # AI-robust: Medium
//
// Detection is type-aware (typeseval.ResolvePackageRef via *types.Info) and
// covers all three import forms (qualified, alias, dot-import). The exempt
// set is single-source (passFunnelPermanentExempt) with cross-validation in
// TestPassFunnelGuardListSync. Not Hard because Go allows arbitrary aliasing;
// the detector requires *types.Info resolve rather than string-matching, so
// it cannot be bypassed by renaming an import alias.
//
// # Blind spots (per ai-robust.md Medium evidence requirement)
//
//   - Value indirection via a local variable (`f := typeseval.ResolvePackageRef;
//     f(...)`): the RHS SelectorExpr IS detected (trips the rule at assignment),
//     but the subsequent call via the variable is not detected. This is the same
//     Soft escape acknowledged in PASS-FUNNEL-EACHFILE-01; accepted because the
//     initial value reference itself trips the rule.
//   - Cross-file indirection (helper assigned in one file, called in another):
//     not detected without inter-procedural analysis. No such pattern exists in
//     production archtest today.
//   - Struct literal `scanner.ImportBan{...}` vs function call `scanner.ImportBan(...)`:
//     both produce SelectorExpr nodes; ResolvePackageRef resolves the X ident to
//     *types.PkgName in both cases, so BOTH are detected correctly (CompositeLit
//     uses the same SelectorExpr shape as a function call).
//
// # Per-form fixture coverage
//
// The 9 typeseval helper symbols are fixtured in two import forms (qualified +
// alias) only. A typeseval dot-import fixture is not present: conflicting imports
// (a file can only dot-import a given package path once, but the package is
// already imported under both a qualified and alias form in redfixture.go) make
// a typeseval dot-import infeasible in the same file. This is NOT a detector gap:
// the dot-import (bare-Ident) form for functions is covered by *types.Func
// resolution inside typeseval.ResolvePackageRef's resolveBarePkgSymbol helper
// (the same path that always handled dot-imported functions), as verified by
// typeseval's own test suite (call_target_test.go TestResolvePackageRef_DotImportBareIdent).
//
// scanner.ImportBan dot-import IS fixtured in all 3 forms (`. "…/scanner"` is
// present alongside the qualified/alias forms in redfixture.go). Post-fix the
// *types.TypeName branch in resolveBarePkgSymbol resolves the bare-Ident form
// `ImportBan{}` to (scannerPkgPath, "ImportBan", true) — matching exactly what
// the qualified SelectorExpr `scanner.ImportBan{}` returns.
//
// TestPassFunnel_FixtureCoverage enforces:
//   - typeseval-helper diagnostics ≥ 2 (qualified + alias forms fixtured)
//   - scanner.ImportBan diagnostics == 3 (qualified + alias + dot-import; exact
//     count locks out any single-form regression including the TypeName fix)
//
// Reverse self-check: TestPassFunnel_FixtureCoverage asserts exact count on
// ImportBan and minimum on typeseval helpers, locking the detector at live-AST
// level. Reverting the *types.TypeName fix in call_target.go drops
// scannerImportBanCount from 3 to 2, failing the assertion.
func diagsResolveHelpers(tgt passFunnelTarget) []scanner.Diagnostic {
	const replacement = "archtest.{ResolvePackageRef,ResolveMethodCall,ResolveEnclosingFunc," +
		"EvaluateConstString,FlatNonDefaultTags,KnownNonDefaultTags,WalkFuncDecls," +
		"WalkFuncDeclsAST,IsCallToPkgFunc,HasReceiver,FuncDeclContext} / " +
		"Pass.{IsFileInScope,IsGenerated} / archtest.ImportBan"
	return scanForForbiddenCallees(
		tgt,
		map[string]map[string]bool{
			typesevalPkgPath: {
				"ResolvePackageRef":     true,
				"ResolveMethodCall":     true,
				"ResolveEnclosingFunc":  true,
				"EvaluateConstString":   true,
				"FlatNonDefaultTags":    true,
				"KnownNonDefaultTags":   true,
				"ParseBuildConstraint":  true,
				"IsGeneratedRelPath":    true,
				"BuildContextPredicate": true,
			},
			scannerPkgPath: {
				"ImportBan": true,
			},
			// callresolver convenience layer (internal/callresolver). Business
			// *_test.go must use the archtest.{WalkFuncDecls,WalkFuncDeclsAST,
			// IsCallToPkgFunc,HasReceiver} façade, not import the internal pkg.
			// WalkFuncDeclsAST is façade-only (no internal symbol of that name),
			// so it is not banned here. FuncDeclContext (the *types.TypeName
			// struct-literal ref) is banned alongside the funcs, mirroring how
			// scanner.ImportBan is banned as a TypeName.
			//
			// Only DIRECT internal-pkg refs trip this: the façade alias
			// `archtest.FuncDeclContext = callresolver.FuncDeclContext` used
			// inside package archtest resolves (via types.Info.Uses) to
			// archtestPkgPath, NOT callresolverPkgPath, so same-package façade
			// use (e.g. a `FuncDeclContext{...}` literal in an archtest rule) is
			// intentionally NOT flagged — that is the sanctioned path.
			//
			// AI-robust grade: Medium (extends the existing PASS-FUNNEL-RESOLVE-01
			// Medium funnel with more symbols — NOT a new funnel). Downstream:
			// type-aware ResolvePackageRef + red-fixture per-symbol lock.
			// Upstream: Go's internal-import visibility ceiling (a sibling
			// *_test.go in package archtest CAN import internal/callresolver), the
			// same permanent ceiling documented for the typeseval-helper ban
			// above — no low-cost Hard path exists, so no new gh-issue is opened.
			callresolverPkgPath: {
				"WalkFuncDecls":   true,
				"IsCallToPkgFunc": true,
				"HasReceiver":     true,
				"FuncDeclContext": true,
			},
		},
		replacement,
	)
}

// fixtureTagSentinelValue is the build-tag string value the detector rejects
// when it appears (in any EvaluateConstString-resolvable form) inside a
// LOADER_SET CallExpr's arg subtree. The value is the same literal as the
// unexported fixtureBuildTag const (fixture.go); declaring it as an unexported
// const inside the framework self-test file lets the detector compare without
// referencing that const (avoiding a circular semantic where the
// detector itself would carry a "loader-arg bypass" pattern its own rule
// would flag if this file were not in passFunnelPermanentExempt).
const fixtureTagSentinelValue = "archtest_fixture"

// fixtureTagLoaderSet enumerates the loader callees (archtest façade typed-scope
// constructors + typeseval package-load primitives) whose argument subtrees the
// detector scans for fixture-tag bypass. EachFileInPackage is intentionally NOT
// in the set: its signature takes an already-loaded *packages.Package, not build
// tags, so it cannot be a fixture-tag bypass vector. Fixture / Run / AST are NOT
// in the set: Fixture's FixtureOpts has no Tags field (type-system Hard), Run
// takes a sealed RunScope (tags already inside the scope), and AST carries no
// TypedOpts at all.
//
// Post-unification (issue #1037 §1d), build tags travel in TypedOpts.Tags, which
// is accepted by the three exported typed-scope constructors Typed / Production /
// StandaloneModule and by the unexported runTypedWithRoot. These replace the
// deleted RunTyped / RunTypedProduction / RunTypedDir entry points.
//
// Adding a new typed-scope constructor that accepts TypedOpts MUST be reflected
// here or the rule silently misses the new vector. The detector godoc lists this
// as an enumeration-maintenance Blind spot (same grade as the banned-symbol list
// in diagsLoadPackages).
var fixtureTagLoaderSet = map[string]map[string]bool{
	archtestPkgPath: {
		// Exported typed-scope constructors (accept TypedOpts with a Tags field).
		"Typed":            true,
		"Production":       true,
		"StandaloneModule": true,
		// runTypedWithRoot is the UNEXPORTED shared impl called by the typed-scope
		// constructors and by Run's dispatch. A business *_test.go in package
		// archtest can call it directly with the fixture tag — sidestepping the
		// FixtureOpts-has-no-Tags compile lock — so it must be enumerated here
		// (#944). ResolvePackageRef resolves the same-package bare-Ident callee to
		// (archtestPkgPath, "runTypedWithRoot"); covered by Form E in
		// passfunnel_inpkg_redfixture.go (cross-package fixtures cannot reference
		// the unexported symbol).
		"runTypedWithRoot": true,
	},
	typesevalPkgPath: {
		"SharedResolver":          true,
		"SharedWorkspaceResolver": true,
		"LoadPackages":            true,
		"LoadProductionPackages":  true,
	},
}

// diagsFixtureTagBypass is the pure detector for PASS-FUNNEL-FIXTURE-TAG-01.
//
// It walks each CallExpr in tgt.file; whenever the callee resolves via
// *types.Info (typeseval.ResolvePackageRef) to a member of
// fixtureTagLoaderSet, it then ast.Inspect-walks every arg subtree and
// reports every Expr whose EvaluateConstString result equals
// "archtest_fixture". This catches all four EvaluateConstString-resolvable
// arg shapes (BasicLit literal / same-pkg const Ident / cross-pkg
// SelectorExpr / BinaryExpr const-concat) uniformly without per-shape code
// branches — the helper already encodes the resolution lattice.
//
// # AI-robust: archtest-bound (callee, arg) form-uniqueness Hard
//
// Charter §Hard 范本 第 2 条 "typed function call as Hard funnel for
// unbounded operations" (panic(any) range, ai-robust.md) is the template
// this rule isomorphs:
//
//   - panicregister.Approved: (callee resolves via *types.Info to
//     panicregister.Approved) AND (reason arg is *ast.BasicLit STRING).
//   - PASS-FUNNEL-FIXTURE-TAG-01: (callee resolves via *types.Info to a
//     member of fixtureTagLoaderSet) AND (any arg subtree contains an
//     Expr whose EvaluateConstString result equals "archtest_fixture").
//
// Both rules form-unique on a (callee, arg) pair; both rely on
// *types.Info resolution (so import aliases / dot-imports / vendor
// rewrites are immaterial); both have zero "looks like Approved but
// isn't" gray zone. The arg-side predicate of this rule is *broader*
// than Approved's BasicLit-only check — it admits any const-resolvable
// shape — because tag identity in build-tag slices legitimately uses
// const references (e.g., a same-package caller passing the unexported
// fixtureBuildTag is a semantically distinct *legitimate* form when the
// callee is, say, `containsTag(...)`, but a bypass form when the callee
// is a loader).
// The (callee, arg) pair is what disambiguates legitimate identity
// from bypass.
//
// # Funnel double-lock (per ai-robust.md §Funnel 双向锁评级)
//
//   - Downstream / outward Hard: Fixture's FixtureOpts has no
//     Tags field; `Run(t, Fixture(FixtureOpts{Tags: ...}, ...), ...)` is a
//     compile error (type system, not archtest-bound).
//   - Upstream / archtest-bound Hard: this rule rejects any
//     (Typed/Production/StandaloneModule/runTypedWithRoot/typeseval loader)-call +
//     arg-resolves-to-archtest_fixture pair regardless of arg shape,
//     including the same-package unexported runTypedWithRoot callee and the
//     same-file var-indirection arg shape (#944).
//
// Together: business archtest cannot express "load a fixture package
// (or any package with the fixture build tag activated)" via any
// reachable AST path — the only legitimate route is Run(t, Fixture(...), rule),
// whose body injects the tag inside the framework.
//
// # Closed by #944
//
//   - Same-file var-indirection: `var tagSet = []string{fixtureBuildTag};
//     Run(t, Typed(TypedOpts{Tags: tagSet}, ...), rule)`. collectFixtureTagBoundObjects
//     records, file-scoped, every types.Object bound (single `:=` / `=` /
//     `var`) to a slice literal carrying a fixture-tag-resolving element; the
//     Ident walker then reports a loader-arg Ident that resolves to such an
//     object. Covered by Form F (passfunnelfixture/redfixture.go) with two
//     GREEN-parity negatives (non-fixture var → loader, fixture tag →
//     non-LOADER_SET callee) under an exact-count lock.
//   - Same-package unexported runTypedWithRoot: now in fixtureTagLoaderSet.
//     Covered by Form E (passfunnel_inpkg_redfixture.go, in-package because the
//     symbol is unexported). Note also: the cross-package selector form
//     (`pkg.FixtureBuildTag` fed to a loader) is type-system-Hard, not
//     archtest-bound — FixtureBuildTag is unexported (#944), so it is a compile
//     error outside package archtest.
//
// # Blind spots (accepted, same grade as PASS-FUNNEL-LOADPACKAGES-01)
//
//   - Multi-RHS positional binding (`a, t := x, []string{fixtureBuildTag}`):
//     collectFixtureTagBoundObjects recognizes single-binding shape only.
//     Same narrow accepted sub-gap as taggroup BS-4. Inter-procedural Hard
//     upgrade tracked via gh issue #973 (see ADR 202605141519 §#944).
//   - Cross-func var escape: a var assigned in one function and read in
//     another, or a closure capture, falls outside the AST walk's reach.
//     Same accept as the sister rules' identical Blind spot.
//   - Reflect / runtime string construction: outside Go static AST scope
//     by definition; the entire archtest framework operates on
//     compile-time AST + types.Info.
//   - LOADER_SET maintenance: adding a new loader API without updating
//     fixtureTagLoaderSet silently misses the new vector. Same grade as
//     the banned-symbol-list maintenance in diagsLoadPackages /
//     diagsResolveHelpers; review-bound.
//   - Tag value also passed as a *non-Tags* loader arg (e.g., as a
//     pattern, modRoot, or modulePath argument that happens to equal
//     "archtest_fixture"): the detector over-reports here because it
//     scans ALL arg subtrees of a loader CallExpr, not only the Tags
//     position. Over-detection is the conservative direction for Hard
//     (false positives are reviewer-visible; false negatives are silent
//     bypass). No realistic loader call passes "archtest_fixture" as a
//     pattern / root / modulePath, so this is theoretical.
//
// # Per-form fixture coverage (TestPassFunnel_FixtureCoverage)
//
// Cross-package forms in internal/passfunnelfixture/redfixture.go exercise the
// EvaluateConstString-resolvable arg shapes plus the var-indirection shape
// against typeseval.SharedResolver (arg-shape coverage, rule predicate is
// callee-shape-agnostic) and archtest.Typed (new-callee coverage, issue #1037):
//
//   - Form A — BasicLit "archtest_fixture" direct       (typeseval.SharedResolver)
//   - Form B — same-pkg const Ident (localFixtureTag)   (typeseval.SharedResolver)
//   - Form C — BinaryExpr "archtest" + "_fixture"       (typeseval.SharedResolver)
//   - Form F — same-file var bound to a fixture-tag slice (var-indirection, typeseval.SharedResolver)
//   - Form G — BasicLit "archtest_fixture" via archtest.Typed (per-member callee, issue #1037)
//   - Form H — BasicLit "archtest_fixture" via archtest.Production (per-member callee)
//   - Form I — BasicLit "archtest_fixture" via archtest.StandaloneModule (per-member callee)
//
// The former Form D (cross-pkg SelectorExpr archtest.FixtureBuildTag) is GONE:
// fixtureBuildTag is unexported (#944), so that vector is a compile error
// outside package archtest (type-system-Hard, no RED fixture). The detector's
// SelectorExpr walker is retained as defense-in-depth.
//
// Form E (same-package unexported runTypedWithRoot) lives in-package
// (passfunnel_inpkg_redfixture.go) and is asserted via a dedicated
// package-archtest load. TestPassFunnel_FixtureCoverage asserts each form
// produces a diagnostic independently (per-form trip-wire) AND an exact-count
// lock over the cross-package forms so the two GREEN-parity negatives are
// load-bearing; removing any single form's fixture line fails exactly that
// form's assertion.
//
// # Exempt
//
// passFunnelPermanentExempt (3 framework files: pass_funnel_test.go +
// pass_test.go + archtest_test.go) — these implement or directly test the
// funnel machinery and must reference the literal by structural necessity.
// Non-_test.go files are excluded from the scan automatically because
// loadPassFunnelTargets filters to *_test.go files: this covers both fixture.go
// (declares fixtureBuildTag) and passfunnel_inpkg_redfixture.go (the Form E
// in-package fixture, which intentionally calls runTypedWithRoot with the tag).
func diagsFixtureTagBypass(tgt passFunnelTarget) []scanner.Diagnostic {
	info := tgt.pkg.TypesInfo
	fset := tgt.pkg.Fset
	var diags []scanner.Diagnostic
	// Var-indirection closure (#944): collect same-file objects bound to a
	// slice literal carrying a fixture-tag-resolving element, so a loader arg
	// that is a plain *ast.Ident (the var name) is still caught. Mirrors
	// collectKnownTagsBoundObjects in taggroup_loop_no_typed_run_invariants.go.
	boundObjs := collectFixtureTagBoundObjects(info, tgt.file)
	// Position-based dedup: a single arg expression may contain nested Exprs
	// that all resolve to the same const value (e.g. BinaryExpr "X"+"Y" plus
	// each child Ident if both are typed const), producing multiple
	// EvaluateConstString hits at descendant positions. Reporting only the
	// first hit per source position keeps the diagnostic count proportional
	// to call-site count.
	scanner.EachInSubtree[ast.CallExpr](tgt.file, func(call *ast.CallExpr) {
		path, name, ok := typeseval.ResolvePackageRef(info, call.Fun)
		if !ok {
			return
		}
		names, ok := fixtureTagLoaderSet[path]
		if !ok || !names[name] {
			return
		}
		// Per-call dedup map: line number ⇒ already-reported. Scoped to this
		// call so two distinct loader calls on the same line (rare in
		// well-formatted Go but possible) each report once.
		reportedLine := make(map[int]bool)
		report := func(expr ast.Expr) {
			line := fset.Position(expr.Pos()).Line
			if reportedLine[line] {
				return
			}
			reportedLine[line] = true
			diags = append(diags, scanner.Diagnostic{
				Rel:  tgt.rel,
				Line: line,
				Message: "use archtest.Run(t, Fixture(FixtureOpts{...}, patterns), rule) " +
					"(the only sanctioned fixture-load path; FixtureOpts has no " +
					"Tags field — see tools/archtest/fixture.go) instead of feeding the " +
					"archtest_fixture build tag to " + path + "." + name +
					" (PASS-FUNNEL-FIXTURE-TAG-01)",
			})
		}
		// EvaluateConstString admits any ast.Expr but a single typed Walker N
		// must be a concrete *S (the EachInSubtree generic constraint forbids
		// interface N like *ast.Expr). The shapes below correspond to the
		// EvaluateConstString resolution paths covered by fixtureTagBypassRedForms
		// (Forms A / B / C / D) plus the var-indirection path (Form F) handled
		// in the Ident walker via boundObjs; enumerating them explicitly mirrors
		// the fixture's per-form anchor structure.
		for _, arg := range call.Args {
			scanner.EachInSubtree[ast.BasicLit](arg, func(lit *ast.BasicLit) {
				if v, ok := typeseval.EvaluateConstString(info, lit); ok && v == fixtureTagSentinelValue {
					report(lit)
				}
			})
			scanner.EachInSubtree[ast.Ident](arg, func(id *ast.Ident) {
				if v, ok := typeseval.EvaluateConstString(info, id); ok && v == fixtureTagSentinelValue {
					report(id)
					return
				}
				// Form F (#944): the ident is not const-resolvable but references
				// a same-file var bound to a fixture-tag slice literal.
				if obj := taggroupObjectOf(info, id); obj != nil {
					if _, bound := boundObjs[obj]; bound {
						report(id)
					}
				}
			})
			scanner.EachInSubtree[ast.SelectorExpr](arg, func(sel *ast.SelectorExpr) {
				if v, ok := typeseval.EvaluateConstString(info, sel); ok && v == fixtureTagSentinelValue {
					report(sel)
				}
			})
			scanner.EachInSubtree[ast.BinaryExpr](arg, func(bin *ast.BinaryExpr) {
				if v, ok := typeseval.EvaluateConstString(info, bin); ok && v == fixtureTagSentinelValue {
					report(bin)
				}
			})
		}
	})
	return diags
}

// exprCarriesFixtureTag reports whether expr's subtree contains any
// EvaluateConstString-resolvable node equal to "archtest_fixture" (covering the
// same BasicLit / Ident / SelectorExpr / BinaryExpr lattice as the per-arg
// detector). Used by collectFixtureTagBoundObjects to decide whether a binding's
// RHS carries the fixture tag.
func exprCarriesFixtureTag(info *types.Info, expr ast.Expr) bool {
	found := false
	scanner.EachInSubtree[ast.BasicLit](expr, func(lit *ast.BasicLit) {
		if v, ok := typeseval.EvaluateConstString(info, lit); ok && v == fixtureTagSentinelValue {
			found = true
		}
	})
	scanner.EachInSubtree[ast.Ident](expr, func(id *ast.Ident) {
		if v, ok := typeseval.EvaluateConstString(info, id); ok && v == fixtureTagSentinelValue {
			found = true
		}
	})
	scanner.EachInSubtree[ast.SelectorExpr](expr, func(sel *ast.SelectorExpr) {
		if v, ok := typeseval.EvaluateConstString(info, sel); ok && v == fixtureTagSentinelValue {
			found = true
		}
	})
	scanner.EachInSubtree[ast.BinaryExpr](expr, func(bin *ast.BinaryExpr) {
		if v, ok := typeseval.EvaluateConstString(info, bin); ok && v == fixtureTagSentinelValue {
			found = true
		}
	})
	return found
}

// collectFixtureTagBoundObjects returns the set of types.Object bound — anywhere
// in file via a single `:=` / `=` / `var` — to an expression whose subtree
// carries the fixture tag (e.g. `var tags = []string{fixtureBuildTag}`). This
// closes the var-indirection Blind spot of PASS-FUNNEL-FIXTURE-TAG-01 (#944): at
// a loader call site the Tags arg is then a plain *ast.Ident the const evaluator
// cannot resolve, so diagsFixtureTagBypass traces the binding object instead.
//
// Scope is file-level (the realistic copy template declares the binding in the
// same file as the loader call) and single-binding only (LHS/RHS length 1),
// mirroring collectKnownTagsBoundObjects in taggroup_loop_no_typed_run_invariants.go.
// Only `var` declarations and `:=` / `=` assignments are collected — `const`
// ValueSpecs are deliberately skipped because a const ident is already reported
// via the const path in the Ident walker (it never reaches the bound-object
// check), so collecting it here would be pure redundancy. Multi-RHS positional
// binding (`a, t := x, []string{fixtureBuildTag}`) is the same narrow accepted
// sub-gap as taggroup BS-4; cross-func / cross-file escape is the same accepted
// Blind spot as the sibling rules. Inter-procedural Hard upgrade tracked via gh
// issue #973 (see package godoc / ADR 202605141519 §#944).
func collectFixtureTagBoundObjects(info *types.Info, file *ast.File) map[types.Object]struct{} {
	out := make(map[types.Object]struct{})
	bindIdent := func(id *ast.Ident) {
		if id == nil || id.Name == "_" {
			return
		}
		if obj := taggroupObjectOf(info, id); obj != nil {
			out[obj] = struct{}{}
		}
	}
	scanner.EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return
		}
		if !exprCarriesFixtureTag(info, as.Rhs[0]) {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			bindIdent(id)
		}
	})
	// `var x = []string{...}` only — token.CONST specs are skipped (covered by
	// the const path). EachInSubtree over GenDecl gives access to the Tok that a
	// bare ValueSpec walk cannot see.
	scanner.EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		if gd.Tok != token.VAR {
			return
		}
		scanner.EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			if len(vs.Names) != 1 || len(vs.Values) != 1 {
				return
			}
			if exprCarriesFixtureTag(info, vs.Values[0]) {
				bindIdent(vs.Names[0])
			}
		})
	})
	return out
}

// TestPassFunnelResolve01 — PASS-FUNNEL-RESOLVE-01.
//
// Archtest tools/archtest/<file>_test.go must NOT call the 9 typeseval helper
// symbols (ResolvePackageRef, ResolveMethodCall, ResolveEnclosingFunc,
// EvaluateConstString, FlatNonDefaultTags, KnownNonDefaultTags,
// ParseBuildConstraint, IsGeneratedRelPath, BuildContextPredicate) or
// scanner.ImportBan directly.
// Use the archtest façade instead:
//   - typeseval helpers → archtest.ResolvePackageRef / .ResolveMethodCall /
//     .EvaluateConstString / .FlatNonDefaultTags / .KnownNonDefaultTags
//   - ParseBuildConstraint / BuildContextPredicate → the retained free funcs
//     archtest.ParseBuildConstraint / archtest.BuildContextPredicate (raw
//     constraint.Expr under multiple tag-set predicates); pass.IsFileInScope(f)
//     only for the simple default-context bool case
//   - IsGeneratedRelPath → pass.IsGenerated(f) (free func removed in #1037)
//   - scanner.ImportBan → archtest.ImportBan (type alias, same struct API)
//
// Detection: SelectorExpr / bare Ident walk + typeseval.ResolvePackageRef
// resolves call/value-ref targets via go/types (covers qualified, alias,
// dot-import forms). Exempt: passFunnelPermanentExempt (3 framework files,
// permanent; LegacyAllowlist deleted in Stage 4).
//
// AI-robust: Medium (see diagsResolveHelpers godoc for full evidence).
func TestPassFunnelResolve01(t *testing.T) {
	targets := loadPassFunnelTargets(t)
	var diags []scanner.Diagnostic
	for _, tgt := range targets {
		diags = append(diags, diagsResolveHelpers(tgt)...)
	}
	scanner.Report(t, "PASS-FUNNEL-RESOLVE-01", diags)
}

// TestPassFunnelEachFile01 — PASS-FUNNEL-EACHFILE-01.
//
// Archtest tools/archtest/<file>_test.go must NOT call
// tools/archtest/internal/scanner.EachFile directly. Use archtest.Run
// (AST-only) which dispatches via Pass + Rule, ensuring single driver
// construction and INV-1 defense.
//
// Detection: SelectorExpr / bare Ident walk + typeseval.ResolvePackageRef
// resolves call targets via go/types (covers qualified `scanner.EachFile`,
// dot-imported bare `EachFile`, and aliased forms). Exempt:
// passFunnelPermanentExempt (3 framework files, permanent; LegacyAllowlist
// deleted in Stage 4).
func TestPassFunnelEachFile01(t *testing.T) {
	targets := loadPassFunnelTargets(t)
	var diags []scanner.Diagnostic
	for _, tgt := range targets {
		diags = append(diags, diagsEachFile(tgt)...)
	}
	scanner.Report(t, "PASS-FUNNEL-EACHFILE-01", diags)
}

// TestPassFunnelLoadPackages01 — PASS-FUNNEL-LOADPACKAGES-01.
//
// Archtest tools/archtest/<file>_test.go must NOT call
// tools/archtest/internal/typeseval.LoadPackages, typeseval.SharedResolver,
// typeseval.LoadProductionPackages, or typeseval.EachFileInPackage directly.
// Use Run(t, Typed(...), rule) (full set) or Run(t, Production(...), rule)
// (generated/-excluded set) — both load packages once via the
// singleflight-cached SharedResolver underneath and construct Pass with
// *types.Package (not *packages.Package) so .Syntax is unreachable.
// The funnel is widened to include the production loader (Stage 1.7) and
// EachFileInPackage (#522 review A1, closes ADR termination-criteria §(c)).
//
// Detection: same SelectorExpr / Ident walk as EACHFILE-01.
func TestPassFunnelLoadPackages01(t *testing.T) {
	targets := loadPassFunnelTargets(t)
	var diags []scanner.Diagnostic
	for _, tgt := range targets {
		diags = append(diags, diagsLoadPackages(tgt)...)
	}
	scanner.Report(t, "PASS-FUNNEL-LOADPACKAGES-01", diags)
}

// TestPassFunnelPackagesImport01 — PASS-FUNNEL-PACKAGES-IMPORT-01.
//
// Archtest tools/archtest/<file>_test.go must NOT import
// golang.org/x/tools/go/packages directly. The Pass-Driver paradigm wraps
// packages.Load inside the typed Run drivers (Run(t, Typed(...)) etc.); direct
// imports allow authors to
// reconstruct the INV-1 form by loading packages and pairing pkg.Syntax
// with a pass.TypesInfo from a different load.
//
// Detection: literal import-path scan over file.Imports.
func TestPassFunnelPackagesImport01(t *testing.T) {
	targets := loadPassFunnelTargets(t)
	var diags []scanner.Diagnostic
	for _, tgt := range targets {
		diags = append(diags, diagsPackagesImport(tgt)...)
	}
	scanner.Report(t, "PASS-FUNNEL-PACKAGES-IMPORT-01", diags)
}

// TestPassFunnelFixtureTagBypass01 — PASS-FUNNEL-FIXTURE-TAG-01.
//
// Archtest tools/archtest/<file>_test.go must NOT feed the archtest_fixture
// build tag into a loader's Tags. The only sanctioned fixture-load path is
// Run(t, Fixture(FixtureOpts{...}, patterns), rule) — FixtureOpts
// deliberately lacks a Tags field, so the build tag stays inside the framework
// body.
//
// There is no longer any legitimate business reason to reference the fixture
// tag from Go code (#944): it was removed from the generic tag union, so no
// module-wide Run(t, Typed(TypedOpts{Tags: FlatNonDefaultTags()}, ...), rule)
// scan activates fixture code and there is no "skip the fixture tag group"
// case. The literal lives only in the unexported fixtureBuildTag const
// (fixture.go) and the //go:build directives of fixture packages — both
// framework-internal.
//
// Detection (delegated to diagsFixtureTagBypass): a (callee, arg)-pair walk —
// it fires only when a CallExpr's callee resolves via *types.Info to a member
// of fixtureTagLoaderSet AND an arg subtree carries "archtest_fixture" (as a
// const-resolvable Expr or a same-file var-binding). It is NOT a blanket
// BasicLit walk: a bare "archtest_fixture" literal outside a loader callsite
// is a legitimate identity use and does not fire. Exempt:
// passFunnelPermanentExempt (3 framework files); fixture.go and
// passfunnel_inpkg_redfixture.go are filtered out by loadPassFunnelTargets's
// *_test.go suffix check.
//
// AI-robust: archtest-bound (callee, arg) form-uniqueness Hard, with a residual
// Medium for cross-func/cross-file var escape (gh issue #973) — see
// diagsFixtureTagBypass godoc for full evidence and accepted blind spots.
func TestPassFunnelFixtureTagBypass01(t *testing.T) {
	targets := loadPassFunnelTargets(t)
	var diags []scanner.Diagnostic
	for _, tgt := range targets {
		diags = append(diags, diagsFixtureTagBypass(tgt)...)
	}
	scanner.Report(t, "PASS-FUNNEL-FIXTURE-TAG-01", diags)
}

// TestPassFunnelGuardListSync — ARCHTEST-PASS-FUNNEL guards alignment.
//
// Cross-validates the three sources of truth that must stay in exact
// alignment after Stage 4 cleanup:
//
//   - .golangci.yml archtest-no-direct-packages-load negative globs
//     (must equal passFunnelPermanentExempt exactly — no migration remnants)
//   - actual file system: tools/archtest/*_test.go files that directly
//     import golang.org/x/tools/go/packages (must also equal
//     passFunnelPermanentExempt exactly)
//
// Invariants (single equality assertions, fail-loud on any drift):
//
//   - yaml-exempt == passFunnelPermanentExempt: no stale migration exemptions
//     remain in .golangci.yml, and no new permanent exemptions were added
//     without updating passFunnelPermanentExempt.
//   - packages-import == passFunnelPermanentExempt: no files outside the 3
//     permanent framework files import golang.org/x/tools/go/packages directly.
//
// This guard prevents Stage 4 regression: if archtestmeta is accidentally
// re-introduced or a new file starts importing packages directly, exactly
// one of these assertions fails with a cmp.Diff showing the drift.
func TestPassFunnelGuardListSync(t *testing.T) {
	root := findModuleRoot(t)
	yamlExempt := loadDepguardArchtestExemptions(t, root)
	packagesImport := loadPackagesImporters(t)

	if !maps.Equal(yamlExempt, passFunnelPermanentExempt) {
		t.Errorf("PASS-FUNNEL-GUARD-SYNC: yamlExempt != passFunnelPermanentExempt\n%s",
			cmp.Diff(passFunnelPermanentExempt, yamlExempt))
	}
	if !maps.Equal(packagesImport, passFunnelPermanentExempt) {
		t.Errorf("PASS-FUNNEL-GUARD-SYNC: packagesImport != passFunnelPermanentExempt\n%s",
			cmp.Diff(passFunnelPermanentExempt, packagesImport))
	}
}

// loadDepguardArchtestExemptions parses .golangci.yml and returns the set of
// module-relative slash paths exempted from the archtest-no-direct-packages-load
// depguard rule via "!**/<rel>" negative globs.
func loadDepguardArchtestExemptions(t *testing.T, root string) map[string]bool {
	t.Helper()
	// #nosec G304 -- root is from findModuleRoot (cwd ancestor with go.mod);
	// the file name is a hard-coded constant. archtest reads checked-in repo
	// configuration; treating this as user-controlled input would be a false
	// positive (same pattern as scanner/content.go:53).
	bytes, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	var cfg struct {
		Linters struct {
			Settings struct {
				Depguard struct {
					Rules map[string]struct {
						Files []string `yaml:"files"`
					} `yaml:"rules"`
				} `yaml:"depguard"`
			} `yaml:"settings"`
		} `yaml:"linters"`
	}
	if err := yaml.Unmarshal(bytes, &cfg); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
	}
	rule, ok := cfg.Linters.Settings.Depguard.Rules["archtest-no-direct-packages-load"]
	if !ok {
		t.Fatalf(".golangci.yml: depguard rule archtest-no-direct-packages-load missing")
	}
	out := make(map[string]bool, len(rule.Files))
	const prefix = "!**/tools/archtest/"
	for _, glob := range rule.Files {
		if !strings.HasPrefix(glob, prefix) {
			continue
		}
		out["tools/archtest/"+strings.TrimPrefix(glob, prefix)] = true
	}
	return out
}

// loadPackagesImporters returns the set of module-relative slash paths of
// tools/archtest/*_test.go files that directly import
// golang.org/x/tools/go/packages, as resolved via SharedResolver.
func loadPackagesImporters(t *testing.T) map[string]bool {
	t.Helper()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}
	out := make(map[string]bool)
	bannedQuoted := strconv.Quote(packagesPkgPath)
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			for _, imp := range file.Imports {
				if imp != nil && imp.Path != nil && imp.Path.Value == bannedQuoted {
					out[rel] = true
					break
				}
			}
		}
	}
	return out
}

// TestPassFunnel_FixtureCoverage is the AI-robust "盲区自检" reverse test:
// loads the build-tag-gated red fixture (internal/passfunnelfixture) and
// asserts each of the three rule detectors emits ≥ 1 diagnostic. Removing
// any banned form from redfixture.go turns the relevant assertion red,
// locking the rule pipeline at the live-AST level rather than the data
// level — analogous to SCANNER-FRAMEWORK-USAGE-01's
// InspectorMethodBanLive coverage lock.
//
// # AST forms covered by the fixture
//
// redfixture.go exercises three import shapes for each banned symbol,
// matching the resolution paths inside typeseval.ResolvePackageRef:
//
//   - qualified-import   (`scanner.EachFile`)
//   - alias-import       (`sn.EachFile` after `import sn "…/scanner"`)
//   - dot-import         (`EachFile` after `import . "…/scanner"`, bare Ident)
//
// # Known blind spots
//
// Value indirection through a local variable (`f := scanner.EachFile;
// f(...)`) is NOT detected: ResolvePackageRef resolves the SelectorExpr
// on the RHS of `:=` (caught as a value reference), but the subsequent
// `f(...)` call site looks like a plain Ident bound to a local *types.Var,
// not to a package member. Sister rule SCANNER-FRAMEWORK-USAGE-01 has the
// same Soft escape; closing it Hard would require dataflow analysis
// beyond the SelectorExpr / Ident scan vocabulary that the rest of the
// archtest framework uses. We accept it here as an acknowledged Soft
// escape — the typed initial assignment still trips the rule, so wrapping
// in a variable is a no-op disguise rather than a true bypass.
//
// For PASS-FUNNEL-RESOLVE-01 specifically: the 9 typeseval helpers are
// fixtured in qualified + alias form only (2 forms). A typeseval dot-import
// form is infeasible in redfixture.go (conflicting imports — the package is
// already imported under qualified + alias; Go allows only one dot-import
// per package path per file). This is NOT a detector gap: the bare-Ident
// dot-import form for functions is covered by *types.Func resolution inside
// typeseval.ResolvePackageRef's resolveBarePkgSymbol helper.
// scanner.ImportBan dot-import IS fixtured (3 forms: qualified + alias + dot).
// After the *types.TypeName fix, the dot-import form (bare Ident `ImportBan{}`)
// is genuinely detected via *types.TypeName resolution, not just by proximity
// to the qualified/alias diagnostics.
//
// The per-form assertions below encode this distinction with an exact count
// for ImportBan (== 3) that would drop to 2 if the TypeName fix were reverted.
//
// The fixture is loaded with the "archtest_fixture" build tag (single source:
// the Fixture loader in tools/archtest/fixture.go, whose Run dispatch injects
// the tag); without the tag the
// fixture is invisible and packages.Load returns an empty *.Syntax slice.
func TestPassFunnel_FixtureCoverage(t *testing.T) {
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(
		root, false, []string{"archtest_fixture"},
		"./tools/archtest/internal/passfunnelfixture",
	)
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var fixtureTargets []passFunnelTarget
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			fixtureTargets = append(fixtureTargets, passFunnelTarget{
				rel: rel, file: file, pkg: pkg,
			})
		}
	}
	if len(fixtureTargets) == 0 {
		t.Fatalf("passfunnelfixture loaded with 0 files — archtest_fixture build tag missing or package empty")
	}

	// Basic ≥1 check for EACHFILE-01 and PACKAGES-IMPORT-01.
	basicRules := []struct {
		name string
		fn   func(passFunnelTarget) []scanner.Diagnostic
	}{
		{"PASS-FUNNEL-EACHFILE-01", diagsEachFile},
		{"PASS-FUNNEL-PACKAGES-IMPORT-01", diagsPackagesImport},
	}
	for _, r := range basicRules {
		var diags []scanner.Diagnostic
		for _, tgt := range fixtureTargets {
			diags = append(diags, r.fn(tgt)...)
		}
		if len(diags) == 0 {
			t.Errorf("rule %s detector found 0 diagnostics on red fixture; "+
				"detector likely regressed (or redfixture.go violation removed)",
				r.name)
		}
	}

	// Strengthened per-symbol check for PASS-FUNNEL-LOADPACKAGES-01.
	//
	// Each banned load symbol (LoadPackages, SharedResolver,
	// SharedWorkspaceResolver, LoadProductionPackages, EachFileInPackage) must generate ≥1 diagnostic
	// independently. The ≥1 total check above (now removed from the basicRules
	// loop) would pass even if a single symbol's fixture lines were deleted
	// (the other symbols still fire). The per-symbol assertion locks each
	// symbol independently so removing any fixture line fails exactly that
	// symbol's assertion.
	{
		var lpDiags []scanner.Diagnostic
		for _, tgt := range fixtureTargets {
			lpDiags = append(lpDiags, diagsLoadPackages(tgt)...)
		}
		perSymbol := map[string]int{
			"LoadPackages":            0,
			"SharedResolver":          0,
			"SharedWorkspaceResolver": 0,
			"LoadProductionPackages":  0,
			"EachFileInPackage":       0,
		}
		for _, d := range lpDiags {
			for sym := range perSymbol {
				if strings.Contains(d.Message, sym) {
					perSymbol[sym]++
				}
			}
		}
		for sym, count := range perSymbol {
			if count == 0 {
				t.Errorf("PASS-FUNNEL-LOADPACKAGES-01: symbol %q produced 0 diagnostics on red fixture; "+
					"either the fixture line for this symbol was removed from redfixture.go "+
					"or the detector regressed for this symbol", sym)
			}
		}
	}

	// Strengthened per-symbol + per-form check for PASS-FUNNEL-RESOLVE-01.
	//
	// typeseval helpers: each of the 8 banned symbols must produce ≥ 1
	// diagnostic independently. The previous ≥ 2 total allowed removing the
	// fixture lines for a single helper without failure (the remaining 7 × 2
	// forms still exceed 2). Per-symbol ≥ 1 ensures removing any single
	// helper's fixture lines fails exactly that symbol's assertion.
	//
	// scanner.ImportBan: exactly 3 diagnostics — qualified (L123) + alias
	// (L124) + dot-import (L125). This is an exact-count check so that
	// reverting the *types.TypeName fix in call_target.go causes this test
	// to fail (the dot-import form produces 0 without the fix → count = 2,
	// not 3). If new fixture lines are added, this count must be updated.
	//
	// Each assertion is a distinct per-symbol / per-form regression trip-wire
	// that fails independently.
	var resolveDiags []scanner.Diagnostic
	for _, tgt := range fixtureTargets {
		resolveDiags = append(resolveDiags, diagsResolveHelpers(tgt)...)
	}
	// Per-symbol ≥1 assertion for the 9 typeseval helpers.
	// Diagnostic messages have the form "use X instead of <typesevalPkgPath>.<SymbolName>".
	typesevalHelpers := []string{
		"ResolvePackageRef",
		"ResolveMethodCall",
		"ResolveEnclosingFunc",
		"EvaluateConstString",
		"FlatNonDefaultTags",
		"KnownNonDefaultTags",
		"ParseBuildConstraint",
		"IsGeneratedRelPath",
		"BuildContextPredicate",
	}
	perHelperCount := make(map[string]int, len(typesevalHelpers))
	var scannerImportBanCount int
	for _, d := range resolveDiags {
		switch {
		case strings.Contains(d.Message, typesevalPkgPath):
			for _, sym := range typesevalHelpers {
				if strings.Contains(d.Message, sym) {
					perHelperCount[sym]++
				}
			}
		case strings.Contains(d.Message, scannerPkgPath) && strings.Contains(d.Message, "ImportBan"):
			scannerImportBanCount++
		}
	}
	for _, sym := range typesevalHelpers {
		if perHelperCount[sym] == 0 {
			t.Errorf("PASS-FUNNEL-RESOLVE-01: typeseval helper %q produced 0 diagnostics on red fixture; "+
				"either the fixture lines for this symbol were removed from redfixture.go "+
				"or the detector regressed for this symbol (per-symbol regression lock)",
				sym)
		}
	}
	// Exact-count assertion: 3 forms (qualified + alias + dot-import).
	// Reverting the *types.TypeName fix in call_target.go drops this to 2
	// (dot-import undetected) → test fails.
	const wantImportBanCount = 3
	if scannerImportBanCount != wantImportBanCount {
		t.Errorf("PASS-FUNNEL-RESOLVE-01: scanner.ImportBan diagnostics on red fixture = %d, want %d "+
			"(qualified L123 + alias L124 + dot-import L125 must each trip the detector; "+
			"exact-count regression lock — reverting TypeName fix drops to 2)",
			scannerImportBanCount, wantImportBanCount)
	}

	// callresolver helpers (internal/callresolver). Per-symbol assertion matches
	// the exact "instead of <callresolverPkgPath>.<sym>" clause (the replacement
	// string says "archtest.<sym>", so this does not collide with the helper
	// names embedded in every message's replacement). The 3 funcs each appear in
	// 3 forms (qualified + alias + dot-import) → ≥1 per symbol; FuncDeclContext
	// (the *types.TypeName struct-literal ref) is exact-count == 3 (qualified +
	// alias + dot-import bare Ident), mirroring the ImportBan TypeName 3-form lock.
	callresolverFuncs := []string{"WalkFuncDecls", "IsCallToPkgFunc", "HasReceiver"}
	perCRCount := make(map[string]int, len(callresolverFuncs))
	var crFuncDeclContextCount int
	for _, d := range resolveDiags {
		if strings.Contains(d.Message, callresolverPkgPath+".FuncDeclContext") {
			crFuncDeclContextCount++
			continue
		}
		for _, sym := range callresolverFuncs {
			if strings.Contains(d.Message, callresolverPkgPath+"."+sym) {
				perCRCount[sym]++
			}
		}
	}
	for _, sym := range callresolverFuncs {
		if perCRCount[sym] == 0 {
			t.Errorf("PASS-FUNNEL-RESOLVE-01: callresolver helper %q produced 0 diagnostics on red fixture; "+
				"either the fixture lines for this symbol were removed from redfixture.go "+
				"or the detector regressed for this symbol (per-symbol regression lock)",
				sym)
		}
	}
	const wantCRFuncDeclContextCount = 3
	if crFuncDeclContextCount != wantCRFuncDeclContextCount {
		t.Errorf("PASS-FUNNEL-RESOLVE-01: callresolver.FuncDeclContext diagnostics on red fixture = %d, want %d "+
			"(qualified + alias + dot-import struct-literal refs must each trip the *types.TypeName branch)",
			crFuncDeclContextCount, wantCRFuncDeclContextCount)
	}

	// PASS-FUNNEL-FIXTURE-TAG-01 per-form coverage. Cross-package fixture forms
	// (redfixture.go, package passfunnelfixture):
	//   - Form A — BasicLit "archtest_fixture" direct         (typeseval.SharedResolver)
	//   - Form B — same-pkg const Ident (localFixtureTag)     (typeseval.SharedResolver)
	//   - Form C — BinaryExpr "archtest" + "_fixture"         (typeseval.SharedResolver)
	//   - Form F — same-file var bound to fixture-tag slice   (typeseval.SharedResolver)
	//   - Form G — BasicLit "archtest_fixture" direct         (archtest.Typed, per-member callee)
	//   - Form H — BasicLit "archtest_fixture" direct         (archtest.Production, per-member callee)
	//   - Form I — BasicLit "archtest_fixture" direct         (archtest.StandaloneModule, per-member callee)
	// plus two GREEN-parity negatives (non-fixture-tag var to a loader; fixture
	// tag to a non-LOADER_SET callee) that MUST produce zero diagnostics. The
	// same-package Form E (unexported runTypedWithRoot) is asserted separately
	// below via a dedicated package-archtest load (Form E cannot live in the
	// cross-package fixture — runTypedWithRoot is unexported). The former Form D
	// (cross-pkg SelectorExpr archtest.FixtureBuildTag) is GONE: fixtureBuildTag
	// is unexported (#944), so that vector is type-system-Hard — a compile error,
	// not an archtest finding — and has no RED fixture.
	//
	// Form identification uses the diagnostic Line number to look up the
	// fixture source line (anchor comment + 1), so assertions stay stable under
	// unrelated edits that shift line numbers.
	const redfixtureRel = "tools/archtest/internal/passfunnelfixture/redfixture.go"
	crossPkgForms := []string{"A", "B", "C", "F", "G", "H", "I"}
	var fixtureTagDiags []scanner.Diagnostic
	for _, tgt := range fixtureTargets {
		fixtureTagDiags = append(fixtureTagDiags, diagsFixtureTagBypass(tgt)...)
	}
	if len(fixtureTagDiags) == 0 {
		t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 detector found 0 diagnostics on red fixture; " +
			"detector likely regressed or fixture forms removed from redfixture.go")
		return
	}
	formAnchorLines := lookupFixtureFormAnchorLines(t, fixtureTargets, redfixtureRel, crossPkgForms)
	for _, form := range crossPkgForms {
		anchorLine, ok := formAnchorLines[form]
		if !ok {
			t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 form-anchor lookup: comment "+
				"' Form %s ' not found in %s — fixture godoc structure changed",
				form, redfixtureRel)
			continue
		}
		// The loader call site is on anchorLine+1 (the line immediately
		// following the form anchor comment).
		callLine := anchorLine + 1
		found := false
		for _, d := range fixtureTagDiags {
			if d.Rel == redfixtureRel && d.Line == callLine {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 Form %s (line %d in %s) produced "+
				"0 diagnostics on red fixture; detector regressed for this arg "+
				"shape OR the fixture line was removed",
				form, callLine, redfixtureRel)
		}
	}
	// Exact-count lock: the cross-package fixture has exactly len(crossPkgForms)
	// RED loader call sites; the two GREEN-parity negatives (non-fixture-tag var
	// to a loader; fixture tag to a non-LOADER_SET callee) MUST add zero. A
	// drift (over-detection on a parity line, or a new RED form without updating
	// this count) fails here — making the GREEN negatives load-bearing.
	wantFixtureTagCount := len(crossPkgForms)
	if got := len(fixtureTagDiags); got != wantFixtureTagCount {
		t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 cross-package diagnostics = %d, want %d "+
			"(forms %v each trip once; GREEN-parity negatives must add 0) — "+
			"over-detection regression or fixture form set changed",
			got, wantFixtureTagCount, crossPkgForms)
	}

	// PASS-FUNNEL-FIXTURE-TAG-01 same-package vector (Form E): the unexported
	// runTypedWithRoot is reachable from a business *_test.go in package
	// archtest. The cross-package passfunnelfixture cannot reference it, so the
	// fixture lives in-package (passfunnel_inpkg_redfixture.go, archtest_fixture
	// tag) and is loaded via a dedicated package-archtest load. Non-test package
	// archtest is small (Pass framework only — rules live in *_test.go), so this
	// cold load is cheap.
	{
		const inPkgRel = "tools/archtest/passfunnel_inpkg_redfixture.go"
		inPkgResolver, err := typeseval.SharedResolver(
			root, false, []string{"archtest_fixture"}, "./tools/archtest",
		)
		if err != nil {
			t.Fatalf("typeseval.SharedResolver (in-package Form E): %v", err)
		}
		var inPkgTargets []passFunnelTarget
		for _, pkg := range inPkgResolver.Packages() {
			if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
				continue
			}
			for _, file := range pkg.Syntax {
				rel := pkgFileRel(root, pkg, file)
				if rel != inPkgRel {
					continue
				}
				inPkgTargets = append(inPkgTargets, passFunnelTarget{rel: rel, file: file, pkg: pkg})
			}
		}
		if len(inPkgTargets) == 0 {
			t.Fatalf("in-package Form E fixture %s not loaded — archtest_fixture "+
				"build tag missing or file absent", inPkgRel)
		}
		var formEDiags []scanner.Diagnostic
		for _, tgt := range inPkgTargets {
			formEDiags = append(formEDiags, diagsFixtureTagBypass(tgt)...)
		}
		formEAnchors := lookupFixtureFormAnchorLines(t, inPkgTargets, inPkgRel, []string{"E"})
		anchorLine, ok := formEAnchors["E"]
		if !ok {
			t.Fatalf("PASS-FUNNEL-FIXTURE-TAG-01 Form E anchor ' Form E ' not found in %s", inPkgRel)
		}
		callLine := anchorLine + 1
		found := false
		for _, d := range formEDiags {
			if d.Rel == inPkgRel && d.Line == callLine {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 Form E (line %d in %s) produced 0 "+
				"diagnostics; runTypedWithRoot not in fixtureTagLoaderSet OR the "+
				"fixture line was removed", callLine, inPkgRel)
		}
	}
}

// TestFlatNonDefaultTagsExcludesFixtureTag locks sub-1 of #944: the generic
// build-tag union must NOT carry the fixture build tag. After #944 removed
// {"archtest_fixture"} from typeseval.KnownNonDefaultTags, FlatNonDefaultTags()
// — and thus every business Run(t, Typed(TypedOpts{Tags: FlatNonDefaultTags()}, ...), rule)
// scan — no longer activates fixture-tagged code; the only sanctioned fixture
// loader is Run(t, Fixture(FixtureOpts{...}, patterns), rule). Re-adding the
// tag to KnownNonDefaultTags reopens the FlatNonDefaultTags() bypass (a
// function-call-result tag the detector cannot const-resolve) and fails this
// assertion.
func TestFlatNonDefaultTagsExcludesFixtureTag(t *testing.T) {
	t.Parallel()
	for _, tag := range FlatNonDefaultTags() {
		if tag == fixtureBuildTag {
			t.Fatalf("FlatNonDefaultTags() must not contain the fixture build tag %q: "+
				"it was removed from KnownNonDefaultTags in #944 so module-wide scans "+
				"never load fixture-tagged code. The only sanctioned fixture loader is "+
				"Run(t, Fixture(FixtureOpts{...}, patterns), rule); re-adding the tag "+
				"reopens the FlatNonDefaultTags bypass.",
				fixtureBuildTag)
		}
	}
}

// lookupFixtureFormAnchorLines walks the fixture file's CommentGroups and
// records the line number of each " Form <X> " marker comment (for the forms
// passed in). The per-form coverage assertion in TestPassFunnel_FixtureCoverage
// uses these line numbers to key the detector hits off the actual
// source-positioned fixture lines (anchor + 1), so the assertion remains stable
// under unrelated fixture edits that shift line numbers but preserve the
// comment-anchored call structure.
func lookupFixtureFormAnchorLines(
	t *testing.T,
	fixtureTargets []passFunnelTarget,
	relPath string,
	forms []string,
) map[string]int {
	t.Helper()
	out := make(map[string]int)
	for _, tgt := range fixtureTargets {
		if tgt.rel != relPath {
			continue
		}
		for _, group := range tgt.file.Comments {
			for _, c := range group.List {
				text := c.Text
				for _, form := range forms {
					if strings.Contains(text, "Form "+form+" ") {
						out[form] = tgt.pkg.Fset.Position(c.Pos()).Line
					}
				}
			}
		}
	}
	return out
}

// scanForForbiddenCallees walks tgt.file for any SelectorExpr / bare Ident
// that resolves (via typeseval.ResolvePackageRef) to one of the entries in
// forbidden (keyed by package path → set of symbol names). Each hit
// becomes a diagnostic suggesting the replacement.
func scanForForbiddenCallees(
	tgt passFunnelTarget,
	forbidden map[string]map[string]bool,
	replacement string,
) []scanner.Diagnostic {
	info := tgt.pkg.TypesInfo
	fset := tgt.pkg.Fset
	var diags []scanner.Diagnostic

	// Pre-collect SelectorExpr.Sel idents so the bare-Ident scan does not
	// double-count qualified call sites. Same pattern as SCANNER-FRAMEWORK-USAGE-01.
	selSels := make(map[*ast.Ident]bool)
	scanner.EachInSubtree[ast.SelectorExpr](tgt.file, func(sel *ast.SelectorExpr) {
		if sel.Sel != nil {
			selSels[sel.Sel] = true
		}
	})

	// (A) qualified SelectorExpr: pkg.Symbol(...)
	scanner.EachInSubtree[ast.SelectorExpr](tgt.file, func(sel *ast.SelectorExpr) {
		path, name, ok := typeseval.ResolvePackageRef(info, sel)
		if !ok {
			return
		}
		names, banned := forbidden[path]
		if !banned || !names[name] {
			return
		}
		diags = append(diags, scanner.Diagnostic{
			Rel:  tgt.rel,
			Line: fset.Position(sel.Pos()).Line,
			Message: fmt.Sprintf(
				"use %s instead of %s.%s",
				replacement, path, name,
			),
		})
	})

	// (B) bare Ident: dot-imported Symbol(...) call site.
	scanner.EachInSubtree[ast.Ident](tgt.file, func(id *ast.Ident) {
		if selSels[id] {
			return
		}
		path, name, ok := typeseval.ResolvePackageRef(info, id)
		if !ok {
			return
		}
		names, banned := forbidden[path]
		if !banned || !names[name] {
			return
		}
		diags = append(diags, scanner.Diagnostic{
			Rel:  tgt.rel,
			Line: fset.Position(id.Pos()).Line,
			Message: fmt.Sprintf(
				"use %s instead of %s.%s",
				replacement, path, name,
			),
		})
	})

	return diags
}

// pkgFileRel returns the file path relative to modRoot for a *ast.File whose
// position is owned by pkg.Fset. Used by pass_funnel_test.go which loads
// packages directly via typeseval.SharedResolver (permanent self-exemption).
func pkgFileRel(modRoot string, pkg *packages.Package, file *ast.File) string {
	pos := pkg.Fset.Position(file.Pos())
	if pos.Filename == "" {
		return ""
	}
	abs, err := filepath.Abs(pos.Filename)
	if err != nil {
		return filepath.ToSlash(pos.Filename)
	}
	rel, err := filepath.Rel(modRoot, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// TestArchtestmetaPackageDeleted is a static reverse-lock that fails while
// tools/archtest/internal/archtestmeta/ still exists on disk. Once Stage 4
// deletes the package, the test passes permanently and prevents regression
// (accidental re-introduction of the scaffold directory).
//
// RED until the archtestmeta directory is deleted in Stage 4 (this PR).
func TestArchtestmetaPackageDeleted(t *testing.T) {
	root := findModuleRoot(t)
	archtestmetaDir := filepath.Join(root, "tools", "archtest", "internal", "archtestmeta")
	_, err := os.Stat(archtestmetaDir)
	if err == nil {
		t.Errorf("TestArchtestmetaPackageDeleted: directory %q still exists; "+
			"Stage 4 must delete tools/archtest/internal/archtestmeta/ entirely",
			archtestmetaDir)
	} else if !os.IsNotExist(err) {
		t.Errorf("TestArchtestmetaPackageDeleted: unexpected Stat error: %v", err)
	}
}
