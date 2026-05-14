package archtest

// pass_funnel_test.go — meta-archtest: enforce archtest.Pass funnel.
//
//   - INVARIANT: PASS-FUNNEL-EACHFILE-01
//   - INVARIANT: PASS-FUNNEL-LOADPACKAGES-01
//   - INVARIANT: PASS-FUNNEL-PACKAGES-IMPORT-01
//
// All three rules forbid archtest tools/archtest/<file>_test.go from
// reaching the legacy entry points directly. Authors must use archtest.Run
// (AST-only) / archtest.RunTyped (typed) via the Pass-Driver paradigm.
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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/archtestmeta"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const (
	// passFunnelSelfBasename is the basename of THIS file. The three rules
	// skip it explicitly: the rule definition uses the very entry points it
	// forbids, and the type system has no way to distinguish "rule
	// implementation" from "rule violator". Self-exemption is permanent —
	// it survives stage 4 LegacyAllowlist deletion.
	passFunnelSelfBasename = "pass_funnel_test.go"

	scannerPkgPath   = "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	typesevalPkgPath = "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	packagesPkgPath  = "golang.org/x/tools/go/packages"
)

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
			if filepath.Base(rel) == passFunnelSelfBasename {
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

// TestPassFunnel_FixtureCoverage is the AI-rebust "盲区自检" reverse test:
// loads the build-tag-gated red fixture (internal/passfunnelfixture) and
// asserts each of the three rule detectors emits ≥ 1 diagnostic. Removing
// any banned form from redfixture.go turns the relevant assertion red,
// locking the rule pipeline at the live-AST level rather than the data
// level — analogous to SCANNER-FRAMEWORK-USAGE-01's
// InspectorMethodBanLive coverage lock.
//
// The fixture is loaded with the archtest_fixture build tag (a sister
// convention to inspectorredfixture etc.); without the tag the fixture
// is invisible and packages.Load returns an empty *.Syntax slice.
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

	rules := []struct {
		name string
		fn   func(passFunnelTarget) []scanner.Diagnostic
	}{
		{"PASS-FUNNEL-EACHFILE-01", diagsEachFile},
		{"PASS-FUNNEL-LOADPACKAGES-01", diagsLoadPackages},
		{"PASS-FUNNEL-PACKAGES-IMPORT-01", diagsPackagesImport},
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
