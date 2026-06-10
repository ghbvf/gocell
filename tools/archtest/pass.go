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

// Pass is the rule-execution context constructed by [Run] and passed to every
// [Rule]. It carries the AST file set, the parsed files, and (in typed mode)
// the go/types Package + TypesInfo bound to those files.
//
// One-Pass-per-scope / one-Pass-per-package shape: an AST scope ([AST]) delivers
// a single Pass containing ALL files in scope; a typed scope ([Typed] /
// [Production] / [Fixture] / [StandaloneModule]) delivers one Pass per loaded
// package (test-variant packages are sorted first; dedup by *ast.File pointer
// identity ensures no file appears in two Passes). Rule authors always iterate
// pass.Files — never assume len(pass.Files)==1.
//
// Authors MUST NOT construct *Pass directly; the only legitimate construction
// site is the [Run] driver in this file. This is enforced by:
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
	// ([AST]) this is ALL in-scope files in a single Pass invocation; in
	// typed mode ([Typed] / [Production] / [Fixture] / [StandaloneModule])
	// this is the dedup'd set of files belonging to one loaded package.
	// Note: AST-only mode delivers ALL files in ONE Pass — rule authors
	// must iterate pass.Files and must not assume len(pass.Files)==1.
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
	// when computing pass.Rel(f). In AST-only mode ([AST]) both runAST and
	// collectASTFiles set Abs from the same abs variable used to compute Rel,
	// so the two accessors share a single source of truth with zero new state.
	Abs func(*ast.File) string
}

// Typed reports whether this Pass carries go/types information (i.e. came from
// a typed scope). Equivalent to `pass.Pkg != nil && pass.TypesInfo != nil`.
func (p *Pass) Typed() bool {
	return p.Pkg != nil && p.TypesInfo != nil
}

// Rule is the unit of work executed by [Run]. It receives a driver-constructed
// *Pass and returns the diagnostics it observed. Rules MUST be pure with
// respect to the test process (no goroutines, no file IO outside the supplied
// Pass) so multiple rules can share the same packages load via
// [typeseval.SharedResolver] without coordination.
type Rule func(*Pass) []Diagnostic

// TypedOpts configures the typed scopes [Typed] / [Production] /
// [StandaloneModule]. Tests selects the test-variant load (includes *_test.go
// and synthetic xtest packages) and matches [typeseval.LoadPackages] semantics;
// Tags sets build tags via -tags=a,b,c.
type TypedOpts struct {
	// Tests, when true, loads the test-variant of each pattern. Defaults to
	// false for production-only walks.
	Tests bool
	// Tags is the slice of build tags joined as -tags=a,b,c. Defaults to
	// the default build context when empty.
	Tags []string
}

// RunScope is the sealed descriptor of WHAT [Run] analyzes. Obtain a value ONLY
// from a scope constructor — [AST] (AST-only over a [Scope]), [Typed]
// (typed main-module patterns), [Production] (typed main module with
// <module>/generated/ excluded), [Fixture] (typed archtest_fixture-tagged
// packages), or [StandaloneModule] (typed standalone fixture module).
//
// The sealing method [RunScope] declares is unexported, so no type OUTSIDE
// package archtest can implement it: an external Cell repo (or any non-archtest
// package) cannot forge a production/fixture scope, cannot inject a build tag
// into a fixture scope (the only public entry that loads fixture-tagged code is
// [Fixture], whose body injects the tag), and cannot reconstruct any banned
// load shape at the call site. This is the Hard DOWNSTREAM / package-external
// leg of the seal.
//
// In-package, the seal is Medium, NOT Hard: every GoCell archtest rule lives in
// package archtest (*_test.go), and Go package visibility cannot forbid a
// same-package file from constructing the unexported scope structs directly
// (e.g. `Run(t, typedRunScope{opts: TypedOpts{Tags: …}}, rule)`, which would
// sidestep [Fixture]'s tag injection). That in-package leg is guarded by the
// RUNSCOPE-CONSTRUCTOR-FUNNEL-01 meta-archtest, which type-aware-bans
// scope-struct composite literals outside their five sanctioned constructors
// (it also closes the struct-literal→archtest_fixture bypass that
// PASS-FUNNEL-FIXTURE-TAG-01, being CallExpr-only, does not catch). The
// permanent Go-language ceiling (a same-package test CAN construct the struct;
// Go cannot make that a compile error) matches #851 / #893 / #1282 / #1424; the
// true-Hard upgrade (scope structs + Run dispatch behind tools/archtest/
// internal/driver) is a deliberate won't-do tracked at gh #1485.
//
// Separately, the "single Run entry, no other Run* export" surface property is
// guarded by the ARCHTEST-SINGLE-RUN-ENTRY-01 meta-archtest (Medium — Go cannot
// forbid declaring a new exported func).
//
// RunScope is the rule-DISPATCH descriptor; it is distinct from [Scope] (=
// scanner.Scope), the file-ENUMERATION descriptor consumed by [EachContentFile]
// / [ImportBan.Run] / [AST]. [AST] adapts a [Scope] into a RunScope.
type RunScope interface {
	// isRunScope seals the interface to constructors declared in this package.
	isRunScope()
}

// astRunScope dispatches [Run] in AST-only mode over a file-enumeration scope.
type astRunScope struct{ fs Scope }

// typedRunScope dispatches [Run] in typed mode loading patterns from the main
// module root (resolved via findModuleRoot).
type typedRunScope struct {
	opts     TypedOpts
	patterns []string
}

// productionRunScope dispatches [Run] in typed mode over the main module's
// production package set ONLY (every <module>/generated/ package excluded).
type productionRunScope struct{ opts TypedOpts }

// dirRunScope dispatches [Run] in typed mode loading patterns from the
// standalone module rooted at the absolute path dir.
type dirRunScope struct {
	dir      string
	opts     TypedOpts
	patterns []string
}

func (astRunScope) isRunScope()        {}
func (typedRunScope) isRunScope()      {}
func (productionRunScope) isRunScope() {}
func (dirRunScope) isRunScope()        {}

// AST wraps the file-enumeration [Scope] fs into a dispatch [RunScope] for
// AST-only rule execution. [Run] parses every file in fs into ONE shared
// [token.FileSet] and constructs ONE [Pass] containing all parsed files;
// Pass.Pkg and Pass.TypesInfo are nil.
//
// For rules that need go/types resolution, use [Typed]; for production-only
// loads (generated/ excluded), [Production]; for standalone fixture modules,
// [StandaloneModule].
func AST(fs Scope) RunScope { return astRunScope{fs: fs} }

// Typed builds a typed [RunScope] loading patterns once through the
// process-wide [typeseval.SharedResolver] cache from the main module root.
// [Run] invokes rule with one Pass per loaded package — Files dedup'd via
// *ast.File pointer identity across the regular and ".test" synthetic variants.
//
// For loading a standalone testdata fixture module (one with its own go.mod),
// use [StandaloneModule] instead.
func Typed(opts TypedOpts, patterns []string) RunScope {
	return typedRunScope{opts: opts, patterns: patterns}
}

// Production builds a typed [RunScope] over the main module's production
// package set ONLY — every package whose import path is under
// <module>/generated/ is excluded by the constructor. Choosing Production IS
// the typed choice that makes generated/ exclusion non-bypassable: a Pass it
// yields can never contain a generated/ file, and there is no patterns argument
// to widen the set.
//
// "Production" here means GENERATED-excluded, NOT test-excluded: opts.Tests is
// still honored, so Production(TypedOpts{Tests: true}) loads each package's
// test variant (its *_test.go files) just like [Typed] — the "ONLY" qualifier
// is about the generated/ filter, not about *_test.go. Use Tests: false for a
// hand-written-non-test walk; Tests: true to also see test files.
//
// Use this for rules that reason over hand-written source and must never
// observe codegen output (false-positive risk + duplicated declarations). It is
// the Pass-model successor of typeseval.LoadProductionPackages /
// ProductionResolver: the generated/ filter is applied by the driver, NOT by a
// per-callsite `if pass.IsGenerated(f) { continue }` discipline (which an
// author can forget — a Hard→Soft regression).
//
// AI-robust: downstream Hard (scanning generated/ output is not expressible
// through this scope — a Pass it yields never contains a generated/ file).
// Upstream Medium (honest caveat): a rule author can still write
// Run(t, Typed(opts, []string{"./..."}), rule) + a manual pass.IsGenerated(f)
// skip per file; that form compiles and runs. The Hard "upstream" property
// (violation unrepresentable at the call site) is not achievable without
// sealing the Typed scope, which would break fixture-module and partial-scan
// rules. See PASS-PRODUCTION-UPSTREAM-HARD-01 (#722) for the upstream Hard
// candidate; this constructor does not, on its own, close that gap.
func Production(opts TypedOpts) RunScope {
	return productionRunScope{opts: opts}
}

// StandaloneModule builds a typed [RunScope] loading patterns from the
// standalone module rooted at dir. dir must be an absolute path to a directory
// containing its own go.mod (a fixture module isolated from the main module).
// Patterns are resolved relative to dir.
//
// This is the correct scope for rules that target intentional-violation
// fixtures in testdata/: those fixtures intentionally import or call constructs
// that production archtest rules forbid, and must therefore live in a separate
// module so they do not pollute the main module's build.
//
// Pass.Rel returns paths relative to dir (the fixture module root), not the
// main module root — so "usage.go" rather than
// "tools/archtest/testdata/.../usage.go".
//
// AI-robust: Hard — the three-line Hard defense is preserved unchanged:
//   - Defense #1: Pass.Pkg is still *types.Package (not *packages.Package);
//     rule authors cannot reach .Syntax or reconstruct INV-1 cross-load bugs.
//   - Defense #2: depguard bans archtest *_test.go from directly importing
//     golang.org/x/tools/go/packages; [Run] is the approved funnel.
//   - Defense #3: meta-archtest PASS-FUNNEL-LOADPACKAGES-01 bans direct
//     typeseval.LoadPackages and typeseval.SharedResolver calls; the [Run]
//     driver is the only approved entry for fixture-module loads.
//
// ref: golang.org/x/tools go/analysis/analysistest/analysistest.go
// (analysistest.Run receives dir string as the module root for the test
// programs; same pattern applied here for isolated fixture modules).
func StandaloneModule(dir string, opts TypedOpts, patterns []string) RunScope {
	return dirRunScope{dir: dir, opts: opts, patterns: patterns}
}

// Run is the single rule-dispatch entry point. It executes rule over scope and
// returns the union of every rule invocation's diagnostics; pass the slice to
// [Report] with a rule ID. The t parameter is [testing.TB] (a *testing.T
// satisfies it) so the [StandaloneModule] fatal-path spy tests can drive Run
// with a tbFatalSpy.
//
// scope selects the mode and source. Scope quick-pick:
//   - No go/types needed (pure AST) → [AST]
//   - Need go/types over the main module → [Typed]
//   - Same as Typed but generated/ excluded (hand-written-source rules) → [Production]
//   - Loading archtest_fixture-tagged fixture packages → [Fixture]
//   - Standalone testdata module with its own go.mod → [StandaloneModule]
//
// All failure modes (nil scope, nil rule, module-root not found, load error,
// parse error, non-absolute StandaloneModule dir) fail-loud via t.Fatalf.
//
// Typed rules read pass.TypesInfo (and pass.Pkg) for resolution; AST-only
// helpers ([EachInSubtree] etc.) work unchanged on pass.Files in either mode.
func Run(t testing.TB, scope RunScope, rule Rule) []Diagnostic {
	t.Helper()
	if rule == nil {
		t.Fatalf("archtest.Run: nil rule")
	}
	if scope == nil {
		t.Fatalf("archtest.Run: nil scope; use AST/Typed/Production/Fixture/StandaloneModule")
		return nil
	}
	switch s := scope.(type) {
	case astRunScope:
		return runAST(t, s.fs, rule)
	case typedRunScope:
		return runTypedWithRoot(t, findModuleRoot(t), s.opts, s.patterns, rule)
	case dirRunScope:
		if !filepath.IsAbs(s.dir) {
			t.Fatalf("archtest.Run: StandaloneModule requires an absolute module root, got %q", s.dir)
		}
		return runStandaloneModuleWithRoot(t, s.dir, s.opts, s.patterns, rule)
	case fixtureRunScope:
		return runTypedWithRoot(t, findModuleRoot(t), TypedOpts{
			Tests: s.opts.Tests,
			Tags:  []string{fixtureBuildTag},
		}, s.patterns, rule)
	case productionRunScope:
		return runProduction(t, s.opts, rule)
	default:
		t.Fatalf("archtest.Run: unknown RunScope %T; use AST/Typed/Production/Fixture/StandaloneModule", scope)
		return nil
	}
}

// runAST executes rule in AST-only mode over fs. The driver parses every Go
// file in fs into ONE shared [token.FileSet] and constructs ONE [Pass]
// containing all parsed files; rule is invoked exactly once with that Pass.
// Pass.Pkg and Pass.TypesInfo are nil. Parse errors fail-loud via t.Fatalf.
//
// This one-Pass-per-scope shape is intentionally identical to the typed
// one-Pass-per-package shape — Pass.Files is always the full file slice the
// rule should iterate, never `Files[0]` with implicit length 1. Mode
// disambiguation comes from Pass.Typed() / Pass.Pkg == nil, not from
// Pass.Files length.
func runAST(t testing.TB, fs Scope, rule Rule) []Diagnostic {
	files, fset, rel, abs := collectASTFiles(t, fs)
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

// runProduction executes rule in typed mode over the main module's production
// package set ONLY (every <module>/generated/ package excluded). It resolves
// the module root via [findModuleRoot], reads the module path from go.mod, and
// delegates to [typeseval.LoadProductionPackages]; rule is invoked with one
// Pass per production package (same dedup/ordering as the other typed scopes).
//
// Extracted from [Run] so the dispatch switch stays within the project's
// gocognit budget; the generated/ filter is applied by the driver here, never
// by the rule. Failure modes (module-root not found, go.mod unreadable, load
// error) fail-loud via t.Fatalf.
//
// ref: golang.org/x/tools/go/analysis Pass.Files driver-controlled scope
func runProduction(t testing.TB, opts TypedOpts, rule Rule) []Diagnostic {
	root := findModuleRoot(t)
	modules := findWorkspaceModules(t, root)
	resolver, err := typeseval.LoadProductionPackages(root, modules, opts.Tests, opts.Tags)
	if err != nil {
		t.Fatalf("archtest.Run: Production: LoadProductionPackages(root=%s, modules=%v, tests=%v, tags=%v): %v",
			root, modules, opts.Tests, opts.Tags, err)
	}
	return runRulePasses(root, resolver.Production(), rule)
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
// This matches go/packages' default ParseFile mode used by the typed scopes,
// making both AST-only and typed rules see the same comment data (gap #1 fix).
//
// Extracted from [runAST] so the parse pass has a single, testable function
// that owns FileSet sharing — the property that makes Pass.Files /
// Pass.Fset internally consistent for AST-only rules.
func collectASTFiles(t testing.TB, scope Scope) (
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

// runTypedWithRoot is the shared implementation for the [Typed],
// [StandaloneModule], and [Fixture] scopes. It loads patterns relative to root
// (the module root directory) through [typeseval.SharedResolver] and invokes
// rule with one Pass per loaded package.
//
// Precondition: root must be a non-empty absolute path. The caller is
// responsible for this guarantee — the [Typed] / [Fixture] dispatch satisfies
// it via findModuleRoot, and the [StandaloneModule] dispatch satisfies it via
// the filepath.IsAbs guard. No runtime check is performed here to avoid
// duplicating caller-side enforcement.
//
// Precondition: rule != nil (guaranteed by Run).
func runTypedWithRoot(t testing.TB, root string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	pkgs := loadTypedPackages(t, root, opts, patterns)
	return runRulePasses(root, pkgs, rule)
}

func runStandaloneModuleWithRoot(t testing.TB, root string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	pkgs := loadTypedPackages(t, root, opts, patterns)
	if len(pkgs) == 0 {
		t.Fatalf("archtest.Run: StandaloneModule: SharedResolver(root=%s, tests=%v, tags=%v, patterns=%v): loaded 0 packages",
			root, opts.Tests, opts.Tags, patterns)
	}
	return runRulePasses(root, pkgs, rule)
}

func loadTypedPackages(t testing.TB, root string, opts TypedOpts, patterns []string) []*packages.Package {
	t.Helper()
	if len(patterns) == 0 {
		t.Fatalf("archtest.Run: typed scope (Typed/Fixture/StandaloneModule) requires at least one pattern; got none")
	}
	resolver, err := typeseval.SharedResolver(root, opts.Tests, opts.Tags, patterns...)
	if err != nil {
		t.Fatalf("archtest.Run: SharedResolver(root=%s, tests=%v, tags=%v, patterns=%v): %v",
			root, opts.Tests, opts.Tags, patterns, err)
	}
	return resolver.Packages()
}

// runRulePasses is the shared Pass-construction loop for [runTypedWithRoot]
// and [runProduction]. It sorts loaded so test-variant packages are
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
// packages.Load when Tests=true). Used to order test variants ahead of regular
// packages so dedup-by-*ast.File yields a deterministic Pass distribution: the
// .test pkg receives every file it owns, the regular pkg is wholly skipped if
// its files were all in the .test view.
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
// already consumed by an earlier pkg via seen). Extracted from the typed driver
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
