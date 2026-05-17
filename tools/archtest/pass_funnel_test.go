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
// tag" in business call sites, but RunTyped still accepts arbitrary Tags,
// so a business archtest could in principle pass []string{"archtest_fixture"}
// to RunTyped / typeseval.SharedResolver directly and bypass RunTypedFixture.
// The rule rejects any BasicLit STRING "archtest_fixture" in business
// archtest *_test.go (passFunnelPermanentExempt scope), driving load sites
// through RunTypedFixture and tag-identity sites through
// archtest.FixtureBuildTag (the typed-reference accessor declared in
// fixture.go). See docs/architecture/202605141519-adr-archtest-pass-funnel.md.
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
				packagesPkgPath),
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
// covers all three import forms (qualified, alias, dot-import). The allowlist
// is single-source (LegacyAllowlist) with cross-validation in
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

// diagsFixtureTagBypass is the pure detector for PASS-FUNNEL-FIXTURE-TAG-01.
//
// It walks BasicLit nodes in tgt.file and reports every STRING literal whose
// unquoted value equals "archtest_fixture". The literal is the only AST form
// through which a business archtest *_test.go can express the fixture build
// tag — once detected, all paths to "load fixture by passing []string{...}"
// (façade bypass of RunTypedFixture) and "test for fixture tag in code"
// (bypass of archtest.FixtureBuildTag typed reference) are closed.
//
// # AI-rebust: archtest-bound form-uniqueness Hard
//
// Charter §1 注脚 "typed function call as Hard funnel for unbounded
// operations" (panic(any) range, ai-collab.md) establishes that
// archtest-bound form-uniqueness without compile-time blocking is the
// highest grade reachable in Go for rule shapes where the language permits
// any value: panic(any), build tag string, etc. This rule meets that bar:
//
//   - Form uniqueness: the literal `"archtest_fixture"` is the only AST
//     shape that carries the tag name into a business *_test.go file. The
//     accompanying funnel pieces are:
//       (i) RunTypedFixture's FixtureOpts has no Tags field — `RunTypedFixture
//           (t, FixtureOpts{Tags: ...}, ...)` is a compile error (outward
//           Hard, downstream funnel side).
//       (ii) fixture.go declares the literal exactly once inside the
//           framework (`Tags: []string{"archtest_fixture"}` in
//           RunTypedFixture's body) and exports `FixtureBuildTag` as a
//           typed string const for Go-code paths that need to identify the
//           tag.
//       (iii) This rule (upstream funnel side) rejects any *_test.go
//           BasicLit STRING with the literal — driving load sites through
//           RunTypedFixture and identity sites through FixtureBuildTag.
//   - No "looks like Approved but isn't" gray zone: BasicLit STRING Kind +
//     unquoted == "archtest_fixture" is a clean predicate; any other shape
//     (Ident → const reference, SelectorExpr → exported const) passes
//     because it is NOT a literal carrying the tag value at this call site.
//
// # Blind spots (accepted, mirroring PASS-FUNNEL-LOADPACKAGES-01)
//
//   - String concatenation: `"archtest" + "_fixture"` produces two BasicLit
//     STRING nodes whose values are "archtest" and "_fixture"; neither
//     matches the predicate. Accepted — no realistic author writes this
//     shape to disguise a tag literal. Same grade as the cross-func var
//     escape accepted by diagsLoadPackages / diagsResolveHelpers.
//   - %s formatted strings: `fmt.Sprintf("archtest_%s", "fixture")` —
//     BasicLit values are "archtest_%s" and "fixture", neither matches.
//     Same accepted gap as above; no realistic shape.
//   - Reflect / runtime string construction: outside Go static AST scope
//     by definition; the entire archtest framework operates on
//     compile-time AST + types.Info.
//
// # Per-form fixture coverage
//
// internal/passfunnelfixture/redfixture.go includes a single bare-literal
// `_ = "archtest_fixture"` line. TestPassFunnel_FixtureCoverage asserts
// ≥1 PASS-FUNNEL-FIXTURE-TAG-01 diagnostic from the fixture; removing the
// line fails the lock. The literal is intentionally NOT placed in any
// realistic shape (no CallExpr arg, no CompositeLit element) because the
// detector predicate is shape-agnostic — any BasicLit STRING with the
// matching value fires, regardless of surrounding AST context.
//
// # Exempt
//
// passFunnelPermanentExempt (3 framework files: pass_funnel_test.go +
// pass_test.go + archtest_test.go) — these implement or directly test the
// funnel machinery and must reference the literal by structural necessity.
// fixture.go is excluded from the scan automatically because
// loadPassFunnelTargets filters to *_test.go files (line 138).
func diagsFixtureTagBypass(tgt passFunnelTarget) []scanner.Diagnostic {
	const tagLiteral = `"archtest_fixture"`
	fset := tgt.pkg.Fset
	var diags []scanner.Diagnostic
	scanner.EachInSubtree[ast.BasicLit](tgt.file, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		if lit.Value != tagLiteral {
			return
		}
		diags = append(diags, scanner.Diagnostic{
			Rel:  tgt.rel,
			Line: fset.Position(lit.Pos()).Line,
			Message: "use archtest.RunTypedFixture (fixture-load path) or " +
				"archtest.FixtureBuildTag (typed-reference path) instead of " +
				`bare "archtest_fixture" literal — façade-bypass funnel closure ` +
				"(PASS-FUNNEL-FIXTURE-TAG-01)",
		})
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
		"./tools/archtest/internal/passfunnelfixture")
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

	// PASS-FUNNEL-FIXTURE-TAG-01 coverage: the fixture file contains a single
	// bare-literal `_ = "archtest_fixture"` line, so the detector must emit
	// ≥1 diagnostic. Removing the literal from redfixture.go fails this lock.
	var fixtureTagDiags []scanner.Diagnostic
	for _, tgt := range fixtureTargets {
		fixtureTagDiags = append(fixtureTagDiags, diagsFixtureTagBypass(tgt)...)
	}
	if len(fixtureTagDiags) == 0 {
		t.Errorf("PASS-FUNNEL-FIXTURE-TAG-01 detector found 0 diagnostics on red fixture; " +
			"detector likely regressed (or `_ = \"archtest_fixture\"` line removed from redfixture.go)")
	}
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
				replacement, path, name),
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
				replacement, path, name),
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
