package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// Pass is the rule-execution context constructed by [Run] and [RunTyped] and
// passed to every [Rule]. It carries the AST file set, the parsed files, and
// (in typed mode) the go/types Package + TypesInfo bound to those files.
//
// One-Pass-per-scope / one-Pass-per-package shape: [Run] delivers a single
// Pass containing ALL files in scope; [RunTyped] delivers one Pass per loaded
// package (test-variant packages are sorted first; dedup by *ast.File pointer
// identity ensures no file appears in two Passes). Rule authors always iterate
// pass.Files — never assume len(pass.Files)==1.
//
// Authors MUST NOT construct *Pass directly; the only legitimate construction
// sites are the Run / RunTyped drivers in this file. This is enforced by:
//
//   - depguard rule archtest-no-direct-packages-load (banning external authors
//     from importing internal/scanner / internal/typeseval / packages),
//   - meta-archtest PASS-FUNNEL-EACHFILE-01 / LOADPACKAGES-01 / PACKAGES-IMPORT-01
//     (re-detecting any bypass via type-aware *types.Info resolution).
//
// The Pkg field is intentionally [*types.Package] (go/types stdlib) — NOT
// [*packages.Package] (golang.org/x/tools/go/packages). Authors cannot reach
// .Syntax from *Pass and therefore cannot reconstruct the INV-1 bug class
// (pairing AST nodes from one load with a *types.Info from a different load).
// See docs/architecture/202605141519-adr-archtest-pass-funnel.md §Hard line.
type Pass struct {
	// Fset is the token.FileSet shared by every file in [Files]. Use
	// pass.Fset.Position(node.Pos()) for human-readable line/column.
	Fset *token.FileSet

	// Files is the slice of parsed files for this Pass. In AST-only mode
	// ([Run]) this is exactly one file per Pass invocation; in typed mode
	// ([RunTyped]) this is the dedup'd set of files belonging to one
	// loaded package.
	Files []*ast.File

	// Pkg is the go/types package descriptor — exposes Name / Path / Imports /
	// Scope. Nil in AST-only mode. Intentionally NOT [*packages.Package]:
	// see the package-level Hard-line discussion in scope.go.
	Pkg *types.Package

	// TypesInfo is the go/types resolution table bound to [Files] / [Pkg].
	// Nil in AST-only mode. Use with go/types-aware helpers (info.Types,
	// info.Uses, info.Selections, etc.) for type-aware AST resolution.
	TypesInfo *types.Info

	// Rel returns the module-relative slash path for a file. The file pointer
	// must come from [Files]; behavior is undefined for files from other Passes.
	Rel func(*ast.File) string

	// Abs returns the module-absolute (OS-native) path for a file. The file
	// pointer must come from [Files]; behavior is undefined for files from other
	// Passes. The returned value always satisfies filepath.IsAbs and equals
	// pass.Fset.Position(f.Pos()).Filename — it is the same physical path used
	// when computing pass.Rel(f). In AST-only mode ([Run]) both Run and
	// collectASTFiles set Abs from the same abs variable used to compute Rel,
	// so the two accessors share a single source of truth with zero new state.
	Abs func(*ast.File) string
}

// Typed reports whether this Pass carries go/types information (i.e. came from
// [RunTyped]). Equivalent to `pass.Pkg != nil && pass.TypesInfo != nil`.
func (p *Pass) Typed() bool {
	return p.Pkg != nil && p.TypesInfo != nil
}

// Rule is the unit of work executed by [Run] / [RunTyped]. It receives a
// driver-constructed *Pass and returns the diagnostics it observed. Rules
// MUST be pure with respect to the test process (no goroutines, no file IO
// outside the supplied Pass) so multiple rules can share the same packages
// load via [typeseval.SharedResolver] without coordination.
type Rule func(*Pass) []Diagnostic

// TypedOpts configures [RunTyped]. Tests selects the test-variant load
// (includes *_test.go and synthetic xtest packages) and matches
// [typeseval.LoadPackages] semantics; Tags sets build tags via -tags=a,b,c.
type TypedOpts struct {
	// Tests, when true, loads the test-variant of each pattern. Defaults to
	// false for production-only walks.
	Tests bool
	// Tags is the slice of build tags joined as -tags=a,b,c. Defaults to
	// the default build context when empty.
	Tags []string
}

// Run executes rule in AST-only mode over scope. The driver parses every Go
// file in scope into ONE shared [token.FileSet] and constructs ONE [Pass]
// containing all parsed files; rule is invoked exactly once with that Pass
// and returns its diagnostics. Pass.Pkg and Pass.TypesInfo are nil. Parse
// errors fail-loud via t.Fatalf.
//
// This one-Pass-per-scope shape is intentionally identical to [RunTyped]'s
// one-Pass-per-package shape — Pass.Files is always the full file slice
// the rule should iterate, never `Files[0]` with implicit length 1. Mode
// disambiguation comes from Pass.Typed() / Pass.Pkg == nil, not from
// Pass.Files length. Rule authors write:
//
//	diags := archtest.Run(t, archtest.ModuleScope(root), func(p *archtest.Pass) []archtest.Diagnostic {
//	    var d []archtest.Diagnostic
//	    for _, file := range p.Files {
//	        archtest.EachInSubtree[ast.CallExpr](file, func(c *ast.CallExpr) {
//	            // … inspect c, append to d, optionally use p.Rel(file) for diag rel
//	        })
//	    }
//	    return d
//	})
//	archtest.Report(t, "MY-RULE-01", diags)
//
// For rules that need go/types resolution, use [RunTyped] instead. For
// production-only loads (generated/ excluded), use [RunTypedProduction]. For
// standalone fixture modules with their own go.mod, use [RunTypedDir].
func Run(t *testing.T, scope Scope, rule Rule) []Diagnostic {
	t.Helper()
	if rule == nil {
		t.Fatalf("archtest.Run: nil rule")
	}
	files, fset, rel, abs := collectASTFiles(t, scope)
	if len(files) == 0 {
		return nil
	}
	pass := &Pass{
		Fset:  fset,
		Files: files,
		Rel:   rel,
		Abs:   abs,
	}
	return rule(pass)
}

// collectASTFiles enumerates Go files in scope, parses every file into a
// single shared *token.FileSet, and returns the parsed *ast.File slice plus
// closures mapping any of those files back to its module-relative slash path
// (rel) and its module-absolute OS-native path (abs). Parse errors fail-loud
// via t.Fatalf, matching scanner.EachFile.
//
// The abs closure returns the same value as fset.Position(f.Pos()).Filename
// (set by parser.ParseFile from the filename argument). Both rel and abs are
// computed from the same abs variable inside the loop — single source of
// truth, zero additional state.
//
// ParseComments is set (|parser.ParseComments) so that comment groups —
// including // INVARIANT: anchors — are present in the returned File.Comments.
// This matches go/packages' default ParseFile mode used by RunTyped, making
// both AST-only and typed rules see the same comment data (gap #1 fix).
//
// Extracted from [Run] so the parse pass has a single, testable function
// that owns FileSet sharing — the property that makes Pass.Files /
// Pass.Fset internally consistent for AST-only rules.
func collectASTFiles(t *testing.T, scope Scope) (
	[]*ast.File, *token.FileSet, func(*ast.File) string, func(*ast.File) string,
) {
	t.Helper()
	paths, err := scope.Files()
	if err != nil {
		t.Fatalf("archtest.Run: scope.Files: %v", err)
	}
	if len(paths) == 0 {
		noop := func(*ast.File) string { return "" }
		return nil, nil, noop, noop
	}
	root := scope.ModRoot()
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(paths))
	relMap := make(map[*ast.File]string, len(paths))
	absMap := make(map[*ast.File]string, len(paths))
	for _, absPath := range paths {
		f, parseErr := parser.ParseFile(fset, absPath, nil,
			parser.SkipObjectResolution|parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("archtest.Run: parse %s: %v", absPath, parseErr)
		}
		files = append(files, f)
		absMap[f] = absPath
		if root != "" {
			if r, relErr := filepath.Rel(root, absPath); relErr == nil {
				relMap[f] = filepath.ToSlash(r)
				continue
			}
		}
		relMap[f] = filepath.ToSlash(absPath)
	}
	return files, fset,
		func(f *ast.File) string { return relMap[f] },
		func(f *ast.File) string { return absMap[f] }
}

// RunTyped executes rule in typed mode. It resolves the module root via
// [findModuleRoot] and delegates to [runTypedWithRoot]. Patterns are loaded
// once through the process-wide [typeseval.SharedResolver] cache, then rule
// is invoked with one Pass per loaded package — Files dedup'd via *ast.File
// pointer identity across the regular and ".test" synthetic variants
// (typeseval test-mode load returns both).
//
// Failure modes (module-root not found, load error, no usable packages)
// fail-loud via t.Fatalf. The returned slice is the union of every rule
// invocation's diagnostics; pass it to [Report] with a rule ID.
//
// Typed rules read pass.TypesInfo (and pass.Pkg) for resolution; AST-only
// helpers ([EachInSubtree] etc.) work unchanged on pass.Files.
//
// For loading a standalone testdata fixture module (one with its own go.mod),
// use [RunTypedDir] instead.
func RunTyped(t *testing.T, opts TypedOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	return runTypedWithRoot(t, findModuleRoot(t), opts, patterns, rule)
}

// RunTypedDir executes rule in typed mode, loading packages from the
// standalone module rooted at dir. dir must be an absolute path to a
// directory containing its own go.mod (a fixture module isolated from the
// main module). Patterns are resolved relative to dir.
//
// This is the correct entry point for rules that target intentional-violation
// fixtures in testdata/: those fixtures intentionally import or call
// constructs that production archtest rules forbid, and must therefore live
// in a separate module so they do not pollute the main module's build.
//
// Pass.Rel returns paths relative to dir (the fixture module root), not the
// main module root — so "usage.go" rather than
// "tools/archtest/testdata/.../usage.go".
//
// AI-rebust: Hard — three-line Hard defense is preserved unchanged:
//   - Defense #1: Pass.Pkg is still *types.Package (not *packages.Package);
//     rule authors cannot reach .Syntax or reconstruct INV-1 cross-load bugs.
//   - Defense #2: depguard bans archtest *_test.go from directly importing
//     golang.org/x/tools/go/packages; RunTypedDir is the approved funnel.
//   - Defense #3: meta-archtest PASS-FUNNEL-LOADPACKAGES-01 bans direct
//     typeseval.LoadPackages and typeseval.SharedResolver calls; RunTypedDir
//     is the only new approved entry for fixture-module loads (funnel widened
//     for both typeseval.LoadPackages and typeseval.SharedResolver, not bypassed).
//
// Failure modes (non-absolute dir, load error) fail-loud via t.Fatalf.
// For main-module loads use [RunTyped].
//
// ref: golang.org/x/tools go/analysis/analysistest/analysistest.go
// (analysistest.Run receives dir string as the module root for the test
// programs; same pattern applied here for isolated fixture modules).
func RunTypedDir(t testing.TB, dir string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	if !filepath.IsAbs(dir) {
		t.Fatalf("archtest.RunTypedDir: dir must be absolute module root, got %q", dir)
	}
	return runTypedWithRoot(t, dir, opts, patterns, rule)
}

// RunTypedProduction executes rule in typed mode over the main module's
// production package set ONLY — every package whose import path is under
// <module>/generated/ is excluded. It resolves the module root via
// [findModuleRoot], reads the module path from go.mod, and delegates to
// [typeseval.LoadProductionPackages]; rule is invoked with one Pass per
// production package (same dedup/ordering as [RunTyped]).
//
// The t parameter is [*testing.T] (not [testing.TB]) because main-module
// loads are never used in fatal-path spy scenarios (unlike [RunTypedDir],
// which accepts [testing.TB] to enable tbFatalSpy unit tests).
//
// Use this for rules that reason over hand-written source and must never
// observe codegen output (false-positive risk + duplicated declarations).
// It is the Pass-model successor of typeseval.LoadProductionPackages /
// ProductionResolver: the generated/ filter is applied by the driver, NOT by
// a per-callsite `if pass.IsGenerated(f) { continue }` discipline (which an
// author can forget — a Hard→Soft regression).
//
// AI-rebust: downstream Hard / upstream Medium.
//
//   - Downstream Hard: scanning generated/ output is NOT EXPRESSIBLE through
//     this entry — a Pass it yields never contains a generated/ file. The
//     three-line Hard defense is unchanged:
//     Defense #1: Pass.Pkg is *types.Package (not *packages.Package).
//     Defense #2: depguard bans archtest *_test.go from importing
//     golang.org/x/tools/go/packages; this driver is the approved funnel.
//     Defense #3: meta-archtest PASS-FUNNEL-LOADPACKAGES-01 /
//     PRODUCTION-LOADER-FUNNEL-01 ban direct typeseval.LoadProductionPackages
//     / SharedResolver calls in business *_test.go; RunTypedProduction is the
//     only legitimate production-load funnel (funnel widened, not bypassed).
//
//   - Upstream Medium (honest caveat): a rule author can still write
//     RunTyped(t, opts, []string{"./..."}, rule) + manual pass.IsGenerated(f)
//     skip per file. That form compiles and runs; generated/ files are present
//     in the Pass but skipped per-file. It is not enforced to route through
//     RunTypedProduction. The Hard "upstream" property (violation unrepresentable
//     at the call site) is not achievable without sealing the RunTyped API,
//     which would break fixture-module and partial-scan rules. Tracked as
//     backlog item PASS-PRODUCTION-UPSTREAM-HARD-01.
//
// Failure modes (module-root not found, go.mod unreadable, load error)
// fail-loud via t.Fatalf. For the full set including generated/, use
// [RunTyped]; for standalone fixture modules, [RunTypedDir].
//
// ref: golang.org/x/tools/go/analysis Pass.Files driver-controlled scope
func RunTypedProduction(t *testing.T, opts TypedOpts, rule Rule) []Diagnostic {
	t.Helper()
	if rule == nil {
		t.Fatalf("archtest.RunTypedProduction: nil rule")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("archtest.RunTypedProduction: read module path: %v", err)
	}
	resolver, err := typeseval.LoadProductionPackages(root, modPath, opts.Tests, opts.Tags)
	if err != nil {
		t.Fatalf("archtest.RunTypedProduction: LoadProductionPackages: %v", err)
	}
	return runRulePasses(root, resolver.Production(), rule)
}

// runTypedWithRoot is the shared implementation for [RunTyped] and
// [RunTypedDir]. It loads patterns relative to root (the module root
// directory) through [typeseval.SharedResolver] and invokes rule with one
// Pass per loaded package.
//
// Precondition: root must be a non-empty absolute path. The caller is
// responsible for this guarantee — RunTyped satisfies it via findModuleRoot,
// and RunTypedDir satisfies it via the filepath.IsAbs guard. No runtime check
// is performed here to avoid duplicating caller-side enforcement.
//
// Extracted to eliminate duplication: RunTyped supplies root via
// findModuleRoot(t); RunTypedDir supplies root as its explicit dir argument.
// The single driver construction path (buildTypedPass) is unchanged — both
// callers produce the same Pass shape.
func runTypedWithRoot(t testing.TB, root string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	if rule == nil {
		t.Fatalf("archtest.RunTyped/RunTypedDir: nil rule")
	}
	if len(patterns) == 0 {
		t.Fatalf("archtest.RunTyped/RunTypedDir: at least one pattern required")
	}
	resolver, err := typeseval.SharedResolver(root, opts.Tests, opts.Tags, patterns...)
	if err != nil {
		t.Fatalf("archtest.RunTyped/RunTypedDir: SharedResolver: %v", err)
	}
	return runRulePasses(root, resolver.Packages(), rule)
}

// runRulePasses is the shared Pass-construction loop for [runTypedWithRoot]
// and [RunTypedProduction]. It sorts loaded so test-variant packages are
// visited BEFORE their regular counterparts. The two variants share *_test.go
// file AST pointers (packages.Load reuses parses) AND share non-test files; we
// dedup by *ast.File pointer below. Without the sort, file-to-pkg assignment
// depends on packages.Load iteration order: a regular pkg visited first would
// claim every non-test file via dedup, leaving the .test pkg's Pass with only
// _test.go files plus a TypesInfo that has also seen the regular files
// (consistent — same load). With the sort, .test pkgs claim ALL their files
// (test + non-test) on first visit; regular pkgs are skipped wholly when
// their entire Syntax set is already seen. Either order is
// correctness-equivalent (Pass.Files, Pass.TypesInfo, Pass.Pkg come from one
// load), but the .test-first order is canonical: every Pass a typed rule
// receives is the maximal view (includes _test.go fixtures when
// opts.Tests==true).
func runRulePasses(root string, loaded []*packages.Package, rule Rule) []Diagnostic {
	pkgs := append([]*packages.Package(nil), loaded...)
	sort.SliceStable(pkgs, func(i, j int) bool {
		return isPackageWithTestFiles(pkgs[i]) && !isPackageWithTestFiles(pkgs[j])
	})

	seen := make(map[*ast.File]bool)
	var all []Diagnostic
	for _, pkg := range pkgs {
		pass := buildTypedPass(root, pkg, seen)
		if pass == nil {
			continue
		}
		all = append(all, rule(pass)...)
	}
	return all
}

// isPackageWithTestFiles reports whether pkg's parsed Syntax contains at
// least one *_test.go file (i.e. pkg is the test variant produced by
// packages.Load when Tests=true). Used by RunTyped to order test variants
// ahead of regular packages so dedup-by-*ast.File yields a deterministic
// Pass distribution: the .test pkg receives every file it owns, the
// regular pkg is wholly skipped if its files were all in the .test view.
func isPackageWithTestFiles(pkg *packages.Package) bool {
	if pkg == nil || pkg.Fset == nil {
		return false
	}
	for _, f := range pkg.Syntax {
		if f == nil {
			continue
		}
		name := pkg.Fset.Position(f.Pos()).Filename
		if strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

// buildTypedPass returns a fully-populated typed *Pass for pkg, or nil when
// pkg lacks the required type info or contributes no new files (every file
// already consumed by an earlier pkg via seen). Extracted from [RunTyped]
// to keep the driver's cognitive complexity below the project's gocognit
// budget; behavior is unchanged.
//
// Pass.Abs is populated from fset.Position(f.Pos()).Filename — the same
// physical path already used by newPackageRel to compute Pass.Rel. Both
// accessors share a single source of truth with no additional maps or state.
func buildTypedPass(root string, pkg *packages.Package, seen map[*ast.File]bool) *Pass {
	if pkg == nil || pkg.Types == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
		return nil
	}
	files := make([]*ast.File, 0, len(pkg.Syntax))
	for _, f := range pkg.Syntax {
		if seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil
	}
	fset := pkg.Fset
	return &Pass{
		Fset:      fset,
		Files:     files,
		Pkg:       pkg.Types,
		TypesInfo: pkg.TypesInfo,
		Rel:       newPackageRel(root, fset),
		Abs:       newPackageAbs(fset),
	}
}

// newPackageRel returns a Pass.Rel closure that converts files belonging to
// fset into module-root-relative slash paths. Pass.Rel is documented as
// "undefined for files from other Passes", so callers must not feed files
// owned by a different *token.FileSet.
func newPackageRel(root string, fset *token.FileSet) func(*ast.File) string {
	return func(f *ast.File) string {
		if f == nil {
			return ""
		}
		abs := fset.Position(f.Pos()).Filename
		if abs == "" {
			return ""
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return abs
		}
		return filepath.ToSlash(rel)
	}
}

// newPackageAbs returns a Pass.Abs closure that returns the module-absolute
// OS-native path for a file belonging to fset. The value is always
// fset.Position(f.Pos()).Filename — the same source used by newPackageRel.
// Both closures share the same fset; no additional map or state is created.
func newPackageAbs(fset *token.FileSet) func(*ast.File) string {
	return func(f *ast.File) string {
		if f == nil {
			return ""
		}
		return fset.Position(f.Pos()).Filename
	}
}

// IsFileInScope reports whether f should be processed under the standard build
// context (all GOOS/GOARCH + cgo + release tags, no project-private tags like
// "integration" or "archtest_fixture"). It delegates to
// typeseval.ParseBuildConstraint (extracts the //go:build / // +build directive
// from the file at pass.Abs(f)) and typeseval.BuildContextPredicate (the
// toolchain-default tag set).
//
// Returns true when f has no build constraint, or when its constraint evaluates
// to true under the default predicate. Returns false for files gated by
// project-specific tags (e.g. "integration", "e2e", "archtest_fixture").
//
// For files that need evaluation under a custom extra-tag set (e.g. "integration"),
// use [archtest.BuildContextPredicate] with [archtest.ParseBuildConstraint]
// directly — IsFileInScope always uses the default (no-extra-tags) predicate.
//
// f must come from pass.Files; behavior is undefined for files from other Passes.
func (p *Pass) IsFileInScope(f *ast.File) bool {
	abs := p.Abs(f)
	if abs == "" {
		return true // no path info → treat as in-scope (conservative)
	}
	expr, err := typeseval.ParseBuildConstraint(abs)
	if err != nil || expr == nil {
		// No constraint or parse error → in scope.
		return true
	}
	return expr.Eval(typeseval.BuildContextPredicate())
}

// IsGenerated reports whether f is a codegen output file under the repo's
// generated/ tree. It delegates to typeseval.IsGeneratedRelPath on pass.Rel(f).
//
// Returns true when the file's module-relative path begins with "generated/".
// Returns false (non-generated, conservative) when [Pass.Rel](f) yields the
// absolute fallback path — i.e. when the file is outside the module root and
// filepath.Rel returns the absolute path unchanged; IsGeneratedRelPath will
// not match a "generated/" prefix on an absolute path.
// f must come from pass.Files; behavior is undefined for files from other Passes.
func (p *Pass) IsGenerated(f *ast.File) bool {
	return typeseval.IsGeneratedRelPath(p.Rel(f))
}
