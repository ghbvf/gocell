package archtest

// pass_funnel_test.go — meta-archtest: enforce archtest.Pass funnel.
//
//   - INVARIANT: PASS-FUNNEL-EACHFILE-01
//   - INVARIANT: PASS-FUNNEL-LOADPACKAGES-01
//   - INVARIANT: PASS-FUNNEL-PACKAGES-IMPORT-01
//   - INVARIANT: PASS-FUNNEL-RESOLVE-01
//
// All four rules forbid archtest tools/archtest/<file>_test.go from
// reaching the legacy entry points directly. Authors must use archtest.Run
// (AST-only) / archtest.RunTyped (typed) via the Pass-Driver paradigm, and
// must call the façade helper functions in archtest.ResolvePackageRef /
// ResolveMethodCall / EvaluateConstString / FlatNonDefaultTags /
// KnownNonDefaultTags / Pass.IsFileInScope / Pass.IsGenerated instead of
// importing internal/typeseval directly.
// See docs/architecture/202605141519-adr-archtest-pass-funnel.md.
//
// Migration: files listed in
// tools/archtest/internal/archtestmeta.LegacyAllowlist are exempt for the
// duration of stage 2/3. Each stage 2/3 PR removes one entry from the
// allowlist AND ports the corresponding archtest to archtest.Pass.
// Stage 4 deletes the allowlist entirely; only this file's self-reference
// remains permanently exempt (basename match) because the rule cannot police
// the implementation that enforces it.

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/archtestmeta"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const (
	scannerPkgPath   = "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	typesevalPkgPath = "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	packagesPkgPath  = "golang.org/x/tools/go/packages"
)

// passFunnelPermanentExempt names archtest framework files that import or
// call the banned entry points by structural necessity:
//
//   - pass_funnel_test.go: implements the PASS-FUNNEL meta-archtest, must
//     reference the forbidden symbols.
//   - pass_test.go: unit-tests archtest.Run / RunTyped / buildTypedPass /
//     newPackageRel / isPackageWithTestFiles; the last three accept or
//     construct *packages.Package fixtures by signature.
//
// These exemptions survive stage-4 cleanup. They are checked by both
// the production PASS-FUNNEL detectors (skip these files entirely) AND
// TestPassFunnelGuardListSync (treats them as "expected yaml exemptions
// even though they are absent from archtestmeta.LegacyAllowlist").
var passFunnelPermanentExempt = map[string]bool{
	"tools/archtest/pass_funnel_test.go": true,
	"tools/archtest/pass_test.go":        true,
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
// tools/archtest/<file>_test.go direct children, and applies self + legacy
// allowlist exemptions. The returned slice is what the three rules consume.
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
			if archtestmeta.LegacyAllowlist[rel] {
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
func diagsLoadPackages(tgt passFunnelTarget) []scanner.Diagnostic {
	return scanForForbiddenCallees(
		tgt,
		map[string]map[string]bool{
			typesevalPkgPath: {
				"LoadPackages":   true,
				"SharedResolver": true,
			},
		},
		"archtest.RunTyped",
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
// Reverse self-check: TestPassFunnel_FixtureCoverage asserts diagsResolveHelpers
// emits ≥ 1 diagnostic on the redfixture, locking the detector at live-AST
// level rather than data level.
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
// dot-import forms). Exempt: self file + pass_test.go (permanent) +
// archtestmeta.LegacyAllowlist (stage 2/3 migration window).
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
// dot-imported bare `EachFile`, and aliased forms). Exempt: self file +
// archtestmeta.LegacyAllowlist.
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
// tools/archtest/internal/typeseval.LoadPackages or
// typeseval.SharedResolver directly. Use archtest.RunTyped which loads
// packages once via the singleflight-cached SharedResolver underneath and
// constructs Pass with *types.Package (not *packages.Package) so .Syntax
// is unreachable.
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

// TestPassFunnelGuardListSync — ARCHTEST-PASS-FUNNEL guards alignment.
//
// Cross-validates the three sources of truth that must stay aligned as the
// stage 2/3 migration drains entries from the migration scaffold:
//
//   - archtestmeta.LegacyAllowlist (Go map, stage-1 baseline 53 entries)
//   - .golangci.yml archtest-no-direct-packages-load negative globs
//     (stage-1 baseline 27 + 1 self-exemption)
//   - actual file system: tools/archtest/*_test.go files that directly
//     import golang.org/x/tools/go/packages
//
// Invariants enforced (fail-loud on any drift):
//
//   - (A) every depguard yaml exemption (except pass_funnel_test.go self) is
//     present in archtestmeta.LegacyAllowlist; otherwise the yaml carries a
//     stale exemption for a file already migrated.
//   - (B) every archtest *_test.go that imports packages directly has a
//     matching depguard yaml exemption; otherwise lint will fail in CI on
//     the full run (not the --new-from-rev mode masking pre-existing diags).
//   - (C) every depguard yaml exemption (except pass_funnel_test.go self)
//     actually imports packages; otherwise the yaml carries a redundant
//     exemption that should have been removed when its file was ported.
//
// This guard replaces the previous "manual sync" contract from the ADR with
// a Hard mechanical check. Drift is a test failure, not a reviewer
// blind-spot.
func TestPassFunnelGuardListSync(t *testing.T) {
	root := findModuleRoot(t)
	yamlExempt := loadDepguardArchtestExemptions(t, root)
	packagesImport := loadPackagesImporters(t)

	// (A) yaml-exempt ∖ passFunnelPermanentExempt ⊆ LegacyAllowlist
	for rel := range yamlExempt {
		if passFunnelPermanentExempt[rel] {
			continue
		}
		if !archtestmeta.LegacyAllowlist[rel] {
			t.Errorf("PASS-FUNNEL-GUARD-SYNC: %q is exempted in .golangci.yml "+
				"archtest-no-direct-packages-load but absent from "+
				"archtestmeta.LegacyAllowlist (stale exemption)", rel)
		}
	}

	// (B) packages-import ⊆ yaml-exempt
	for rel := range packagesImport {
		if !yamlExempt[rel] {
			t.Errorf("PASS-FUNNEL-GUARD-SYNC: %q imports "+
				"golang.org/x/tools/go/packages directly but lacks a "+
				".golangci.yml archtest-no-direct-packages-load exemption "+
				"(full-repo golangci-lint will fail on this file)", rel)
		}
	}

	// (C) yaml-exempt ∖ passFunnelPermanentExempt ⊆ packages-import
	for rel := range yamlExempt {
		if passFunnelPermanentExempt[rel] {
			continue
		}
		if !packagesImport[rel] {
			t.Errorf("PASS-FUNNEL-GUARD-SYNC: %q is exempted in .golangci.yml "+
				"but does not import golang.org/x/tools/go/packages "+
				"(redundant exemption — drop the line)", rel)
		}
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
// The fixture is loaded with the [archtestmeta.FixtureBuildTag] build tag
// (a sister convention to inspectorredfixture etc.); without the tag the
// fixture is invisible and packages.Load returns an empty *.Syntax slice.
func TestPassFunnel_FixtureCoverage(t *testing.T) {
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(
		root, false, []string{archtestmeta.FixtureBuildTag},
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

	rules := []struct {
		name string
		fn   func(passFunnelTarget) []scanner.Diagnostic
	}{
		{"PASS-FUNNEL-EACHFILE-01", diagsEachFile},
		{"PASS-FUNNEL-LOADPACKAGES-01", diagsLoadPackages},
		{"PASS-FUNNEL-PACKAGES-IMPORT-01", diagsPackagesImport},
		{"PASS-FUNNEL-RESOLVE-01", diagsResolveHelpers},
	}
	for _, r := range rules {
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
