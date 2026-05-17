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
// (AST-only) / archtest.RunTyped (typed) / archtest.RunTypedFixture (fixture
// packages) / archtest.RunTypedProduction (production-only) via the
// Pass-Driver paradigm, and must call the façade helper functions in
// archtest.ResolvePackageRef / ResolveMethodCall / EvaluateConstString /
// FlatNonDefaultTags / KnownNonDefaultTags / Pass.IsFileInScope /
// Pass.IsGenerated instead of importing internal/typeseval directly.
//
// PASS-FUNNEL-FIXTURE-TAG-01 closes the façade-bypass leg of the
// archtest_fixture funnel: RunTypedFixture's outward Hard (FixtureOpts has
// no Tags field) prevents typed expression of "load fixture with custom
// tag" in business call sites, but RunTyped / typeseval.SharedResolver
// still accept arbitrary Tags, so a business archtest could in principle
// pass the archtest_fixture build tag (in any const-resolvable form) to a
// loader and bypass RunTypedFixture. The rule rejects any
// (callee, arg) pair where the callee resolves via *types.Info to a member
// of fixtureTagLoaderSet AND any arg subtree contains an Expr that
// EvaluateConstString resolves to "archtest_fixture" — catching BasicLit
// literal / same-pkg const Ident / cross-pkg SelectorExpr / BinaryExpr
// const-concat uniformly. The (callee, arg) pair shape disambiguates
// legitimate identity uses (e.g., containsTag(group, FixtureBuildTag) in
// panic_invariants_test.go — callee not in loader set) from bypass
// (loader callee + same value resolves). Isomorphic to charter §Hard 范本
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
	scannerPkgPath        = "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	archtestPkgPath       = "github.com/ghbvf/gocell/tools/archtest"
	typesevalPkgPath      = "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	packagesPkgPath       = "golang.org/x/tools/go/packages"
	usage02FixturesRelDir = "tools/archtest/internal/usage02fixtures"
)

// passFunnelPermanentExempt names archtest framework files that import or
// call the banned entry points by structural necessity:
//
//   - pass_funnel_test.go: implements the PASS-FUNNEL meta-archtest, must
//     reference the forbidden symbols.
//   - pass_test.go: unit-tests archtest.Run / RunTyped / buildTypedPass /
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
//     Backlog item ARCHTEST-LAYER10-PASS-MIGRATION-01 tracks a future
//     migration of (b)+(c) to the Pass model when a typed-field accessor
//     is available.
//
// These exemptions survive stage-4 cleanup. They are checked by both
// the production PASS-FUNNEL detectors (skip these files entirely) AND
// TestPassFunnelGuardListSync (exact-equality assertions: yaml-exempt ==
// passFunnelPermanentExempt AND packages-import == passFunnelPermanentExempt).
//
// AI-rebust grade: Medium. The 3-entry set is structurally necessary (these
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
		"archtest.Run / archtest.RunTyped",
	)
}

// diagsLoadPackages is the pure detector for PASS-FUNNEL-LOADPACKAGES-01.
// It bans business archtest *_test.go files from directly calling the four
// typeseval package-load symbols: LoadPackages, SharedResolver,
// LoadProductionPackages (Stage 1.7 funnel widen), and EachFileInPackage
// (the INV-1 suppression helper that the Pass funnel replaces — #522 review
// A1, closes ADR termination-criteria §(c)). Detection is type-aware via
// typeseval.ResolvePackageRef on all SelectorExpr / bare Ident nodes.
//
// # AI-rebust: Medium
//
// Detection is type-aware (typeseval.ResolvePackageRef via *types.Info) and
// covers all three import forms (qualified, alias, dot-import). Not Hard
// because Go allows arbitrary aliasing; the detector requires *types.Info
// resolve rather than string-matching, so it cannot be bypassed by renaming
// an import alias.
//
// # Blind spots (per ai-collab.md Medium evidence requirement)
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
// LoadPackages, SharedResolver, LoadProductionPackages, and EachFileInPackage
// are each fixtured in two qualified + alias forms in redfixture.go.
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
func diagsLoadPackages(tgt passFunnelTarget) []scanner.Diagnostic {
	return scanForForbiddenCallees(
		tgt,
		map[string]map[string]bool{
			typesevalPkgPath: {
				"LoadPackages":           true,
				"SharedResolver":         true,
				"LoadProductionPackages": true,
				"EachFileInPackage":      true,
			},
		},
		"archtest.RunTyped / archtest.RunTypedProduction",
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
				"direct import of %q forbidden in archtest *_test.go; use archtest.RunTyped",
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
// # AI-rebust: Medium
//
// Detection is type-aware (typeseval.ResolvePackageRef via *types.Info) and
// covers all three import forms (qualified, alias, dot-import). The exempt
// set is single-source (passFunnelPermanentExempt) with cross-validation in
// TestPassFunnelGuardListSync. Not Hard because Go allows arbitrary aliasing;
// the detector requires *types.Info resolve rather than string-matching, so
// it cannot be bypassed by renaming an import alias.
//
// # Blind spots (per ai-collab.md Medium evidence requirement)
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
// The 8 typeseval helper symbols are fixtured in two import forms (qualified +
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
	const replacement = "archtest.{ResolvePackageRef,ResolveMethodCall,EvaluateConstString," +
		"FlatNonDefaultTags,KnownNonDefaultTags} / Pass.{IsFileInScope,IsGenerated} / archtest.ImportBan"
	return scanForForbiddenCallees(
		tgt,
		map[string]map[string]bool{
			typesevalPkgPath: {
				"ResolvePackageRef":     true,
				"ResolveMethodCall":     true,
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
		},
		replacement,
	)
}

// fixtureTagSentinelValue is the build-tag string value the detector rejects
// when it appears (in any EvaluateConstString-resolvable form) inside a
// LOADER_SET CallExpr's arg subtree. The value is the same literal that
// archtest.FixtureBuildTag exports; declaring it as an unexported const
// inside the framework self-test file lets the detector compare without
// referencing the exported const (avoiding a circular semantic where the
// detector itself would carry a "loader-arg bypass" pattern its own rule
// would flag if this file were not in passFunnelPermanentExempt).
const fixtureTagSentinelValue = "archtest_fixture"

// fixtureTagLoaderSet enumerates the loader callees (archtest façade +
// typeseval package-load primitives) whose argument subtrees the detector
// scans for fixture-tag bypass. EachFileInPackage is intentionally NOT in
// the set: its signature takes an already-loaded *packages.Package, not
// build tags, so it cannot be a fixture-tag bypass vector.
//
// Adding a new loader API (e.g., a hypothetical `RunTypedFixtureDir`) MUST
// be reflected here or the rule silently misses the new vector. The
// detector godoc lists this as an enumeration-maintenance Blind spot
// (same grade as the banned-symbol list in diagsLoadPackages).
var fixtureTagLoaderSet = map[string]map[string]bool{
	archtestPkgPath: {
		"RunTyped":           true,
		"RunTypedProduction": true,
		"RunTypedDir":        true,
	},
	typesevalPkgPath: {
		"SharedResolver":         true,
		"LoadPackages":           true,
		"LoadProductionPackages": true,
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
// # AI-rebust: archtest-bound (callee, arg) form-uniqueness Hard
//
// Charter §Hard 范本 第 2 条 "typed function call as Hard funnel for
// unbounded operations" (panic(any) range, ai-collab.md) is the template
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
// const references (e.g., callers passing archtest.FixtureBuildTag is
// a semantically distinct *legitimate* form when the callee is, say,
// `containsTag(...)`, but a bypass form when the callee is a loader).
// The (callee, arg) pair is what disambiguates legitimate identity
// from bypass.
//
// # Funnel double-lock (per ai-collab.md §Funnel 双向锁评级)
//
//   - Downstream / outward Hard: RunTypedFixture's FixtureOpts has no
//     Tags field; `RunTypedFixture(t, FixtureOpts{Tags: ...}, ...)` is a
//     compile error (type system, not archtest-bound).
//   - Upstream / archtest-bound Hard: this rule rejects any loader-call +
//     arg-resolves-to-archtest_fixture pair regardless of arg shape.
//
// Together: business archtest cannot express "load a fixture package
// (or any package with the fixture build tag activated)" via any
// reachable AST path — the only legitimate route is RunTypedFixture,
// whose body injects the tag inside the framework using archtest.
// FixtureBuildTag (the typed-reference single source).
//
// # Blind spots (accepted, same grade as PASS-FUNNEL-LOADPACKAGES-01)
//
//   - Tags-arg via *ast.Ident binding to a same-file var (not const):
//     `var tagSet = []string{FixtureBuildTag}; RunTyped(.., TypedOpts{
//     Tags: tagSet}, ..)`. The detector ast.Inspect-walks the
//     CompositeLit "[]string{FixtureBuildTag}" *inside the var decl* —
//     if the var decl is in the same file, the FixtureBuildTag
//     SelectorExpr appears as a top-level Expr in the file and is
//     visited; however, that walk is **not** scoped to a loader CallExpr,
//     so the detector does NOT fire there. Within the loader CallExpr,
//     the arg is just an *ast.Ident (the var name), which
//     EvaluateConstString does not resolve (only const Idents resolve).
//     Workaround: review-bound. Same accept grade as cross-func var
//     escape in diagsLoadPackages / diagsResolveHelpers.
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
// internal/passfunnelfixture/redfixture.go's fixtureTagBypassRedForms
// exercises all four EvaluateConstString-resolvable arg shapes against
// typeseval.SharedResolver (one LOADER_SET callee; the rule predicate
// is callee-shape-agnostic across the set):
//
//   - Form A — BasicLit "archtest_fixture" direct
//   - Form B — same-pkg const Ident (localFixtureTag)
//   - Form C — BinaryExpr "archtest" + "_fixture"
//   - Form D — cross-pkg SelectorExpr archtest.FixtureBuildTag
//
// TestPassFunnel_FixtureCoverage asserts each form produces ≥1
// diagnostic independently (per-form trip-wire); removing any single
// form's fixture line fails exactly that form's assertion.
//
// # Exempt
//
// passFunnelPermanentExempt (3 framework files: pass_funnel_test.go +
// pass_test.go + archtest_test.go) — these implement or directly test the
// funnel machinery and must reference the literal by structural necessity.
// fixture.go is excluded from the scan automatically because
// loadPassFunnelTargets filters to *_test.go files (line 138).
func diagsFixtureTagBypass(tgt passFunnelTarget) []scanner.Diagnostic {
	info := tgt.pkg.TypesInfo
	fset := tgt.pkg.Fset
	var diags []scanner.Diagnostic
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
				Message: "use archtest.RunTypedFixture (fixture-load path) " +
					"or archtest.FixtureBuildTag (typed-identity path, only " +
					"outside LOADER_SET callees) instead of feeding the " +
					"archtest_fixture build tag to " + path + "." + name +
					" (PASS-FUNNEL-FIXTURE-TAG-01)",
			})
		}
		// EvaluateConstString admits any ast.Expr but a single typed Walker N
		// must be a concrete *S (the EachInSubtree generic constraint forbids
		// interface N like *ast.Expr). The four shapes below correspond to
		// the four EvaluateConstString resolution paths covered by
		// fixtureTagBypassRedForms (Forms A / B / C / D); enumerating them
		// explicitly mirrors the fixture's per-form anchor structure.
		for _, arg := range call.Args {
			scanner.EachInSubtree[ast.BasicLit](arg, func(lit *ast.BasicLit) {
				if v, ok := typeseval.EvaluateConstString(info, lit); ok && v == fixtureTagSentinelValue {
					report(lit)
				}
			})
			scanner.EachInSubtree[ast.Ident](arg, func(id *ast.Ident) {
				if v, ok := typeseval.EvaluateConstString(info, id); ok && v == fixtureTagSentinelValue {
					report(id)
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

// TestPassFunnelResolve01 — PASS-FUNNEL-RESOLVE-01.
//
// Archtest tools/archtest/<file>_test.go must NOT call the 8 typeseval helper
// symbols (ResolvePackageRef, ResolveMethodCall, EvaluateConstString,
// FlatNonDefaultTags, KnownNonDefaultTags, ParseBuildConstraint,
// IsGeneratedRelPath, BuildContextPredicate) or scanner.ImportBan directly.
// Use the archtest façade instead:
//   - typeseval helpers → archtest.ResolvePackageRef / .ResolveMethodCall /
//     .EvaluateConstString / .FlatNonDefaultTags / .KnownNonDefaultTags
//   - ParseBuildConstraint+BuildContextPredicate → pass.IsFileInScope(f)
//   - IsGeneratedRelPath → pass.IsGenerated(f)
//   - scanner.ImportBan → archtest.ImportBan (type alias, same struct API)
//
// Detection: SelectorExpr / bare Ident walk + typeseval.ResolvePackageRef
// resolves call/value-ref targets via go/types (covers qualified, alias,
// dot-import forms). Exempt: passFunnelPermanentExempt (3 framework files,
// permanent; LegacyAllowlist deleted in Stage 4).
//
// AI-rebust: Medium (see diagsResolveHelpers godoc for full evidence).
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
// Use archtest.RunTyped (full set) or archtest.RunTypedProduction
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
// packages.Load inside archtest.RunTyped; direct imports allow authors to
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
// Archtest tools/archtest/<file>_test.go must NOT contain a bare STRING
// literal "archtest_fixture". The literal is the only AST form through
// which a business archtest can carry the fixture build tag into a call
// site (either as a Tags slice element passed to RunTyped /
// typeseval.SharedResolver to bypass RunTypedFixture, or as a comparison
// target to identify the fixture tag group). Authors must:
//
//   - For fixture loading: call archtest.RunTypedFixture(t, FixtureOpts{...},
//     patterns, rule). FixtureOpts deliberately lacks a Tags field, so the
//     build tag stays inside the framework body.
//   - For tag-identity tests (e.g., skipping the fixture tag group from a
//     module-wide RunTyped scan): reference archtest.FixtureBuildTag, the
//     typed string const exported by fixture.go.
//
// Detection: BasicLit STRING walk with unquoted-value equality on
// "archtest_fixture". Exempt: passFunnelPermanentExempt (3 framework
// files); fixture.go is filtered out by loadPassFunnelTargets's
// *_test.go suffix check.
//
// AI-rebust: archtest-bound form-uniqueness Hard (see diagsFixtureTagBypass
// godoc for full evidence and accepted blind spots).
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

// TestPassFunnel_FixtureCoverage is the AI-rebust "盲区自检" reverse test:
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
// For PASS-FUNNEL-RESOLVE-01 specifically: the 8 typeseval helpers are
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
// RunTypedFixture helper in tools/archtest/fixture.go); without the tag the
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
	// Each of the four banned load symbols (LoadPackages, SharedResolver,
	// LoadProductionPackages, EachFileInPackage) must generate ≥1 diagnostic
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
			"LoadPackages":           0,
			"SharedResolver":         0,
			"LoadProductionPackages": 0,
			"EachFileInPackage":      0,
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
	// Per-symbol ≥1 assertion for the 8 typeseval helpers.
	// Diagnostic messages have the form "use X instead of <typesevalPkgPath>.<SymbolName>".
	typesevalHelpers := []string{
		"ResolvePackageRef",
		"ResolveMethodCall",
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

	// PASS-FUNNEL-FIXTURE-TAG-01 per-form coverage: the fixture function
	// fixtureTagBypassRedForms exercises all 4 EvaluateConstString-resolvable
	// arg shapes (Form A literal / B same-pkg const Ident / C BinaryExpr
	// concat / D cross-pkg SelectorExpr archtest.FixtureBuildTag) inside
	// typeseval.SharedResolver calls. The detector must emit one diagnostic
	// per form (4 total). The per-form trip-wire below asserts each form
	// independently — removing any single fixture call line fails exactly
	// that form's assertion, not just the aggregate count.
	//
	// Form identification uses the diagnostic Line number to look up the
	// fixture source line. The fixture is the only file in the load that
	// can produce diagsFixtureTagBypass hits (no other RED form exists
	// elsewhere in the fixture sub-packages, and pass_funnel_test.go +
	// pass_test.go themselves are in passFunnelPermanentExempt — though
	// the per-form assertion below scopes by file rel to redfixture.go
	// for explicitness).
	const redfixtureRel = "tools/archtest/internal/passfunnelfixture/redfixture.go"
	var fixtureTagDiags []scanner.Diagnostic
	for _, tgt := range fixtureTargets {
		fixtureTagDiags = append(fixtureTagDiags, diagsFixtureTagBypass(tgt)...)
	}
	if len(fixtureTagDiags) == 0 {
		t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 detector found 0 diagnostics on red fixture; " +
			"detector likely regressed or fixtureTagBypassRedForms removed from redfixture.go")
		return
	}
	// Look up the source-line numbers of the 4 form anchors in redfixture.go
	// so the per-form assertions key off the actual file, not hard-coded line
	// numbers (which drift on unrelated edits). The anchor comments " Form A "
	// / " Form B " / " Form C " / " Form D " each precede their respective
	// SharedResolver call by exactly one line.
	formAnchorLines := lookupFixtureFormAnchorLines(t, fixtureTargets, redfixtureRel)
	for _, form := range []string{"A", "B", "C", "D"} {
		anchorLine, ok := formAnchorLines[form]
		if !ok {
			t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 form-anchor lookup: comment "+
				"' Form %s ' not found in %s — fixture godoc structure changed",
				form, redfixtureRel)
			continue
		}
		// The SharedResolver call site is on anchorLine+1 (the line immediately
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
}

// lookupFixtureFormAnchorLines walks the fixture file's CommentGroups and
// records the line number of each " Form A " / " Form B " / " Form C " /
// " Form D " marker comment. The per-form coverage assertion in
// TestPassFunnel_FixtureCoverage uses these line numbers to key the detector
// hits off the actual source-positioned fixture lines (anchor + 1), so the
// assertion remains stable under unrelated fixture edits that shift line
// numbers but preserve the comment-anchored call structure.
func lookupFixtureFormAnchorLines(
	t *testing.T,
	fixtureTargets []passFunnelTarget,
	relPath string,
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
				for _, form := range []string{"A", "B", "C", "D"} {
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
