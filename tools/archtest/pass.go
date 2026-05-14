package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// Pass is the rule-execution context constructed by [Run] and [RunTyped] and
// passed to every [Rule]. It carries the AST file set, the parsed files, and
// (in typed mode) the go/types Package + TypesInfo bound to those files.
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

// Run executes rule in AST-only mode over scope. Each parsed file in scope is
// wrapped in a fresh Pass with [Pass.Pkg] and [Pass.TypesInfo] nil; the rule
// returns its diagnostics, which are accumulated across files and returned to
// the caller. Parse errors fail-loud via t.Fatalf.
//
// Callers issue [Report] on the returned slice with a rule ID:
//
//	diags := archtest.Run(t, archtest.ModuleScope(root), myRule)
//	archtest.Report(t, "MY-RULE-01", diags)
//
// For rules that need go/types resolution, use [RunTyped] instead.
func Run(t *testing.T, scope Scope, rule Rule) []Diagnostic {
	t.Helper()
	if rule == nil {
		t.Fatalf("archtest.Run: nil rule")
	}
	var all []Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(_ *testing.T, fc scanner.FileContext) {
		pass := &Pass{
			Fset:  fc.Fset,
			Files: []*ast.File{fc.File},
			Rel:   func(*ast.File) string { return fc.Rel },
		}
		all = append(all, rule(pass)...)
	})
	return all
}

// RunTyped executes rule in typed mode. It resolves the module root, loads
// patterns once through the process-wide [typeseval.SharedResolver] cache,
// then invokes rule with one Pass per loaded package — Files dedup'd via
// *ast.File pointer identity across the regular and ".test" synthetic
// variants (typeseval test-mode load returns both).
//
// Failure modes (module-root not found, load error, no usable packages)
// fail-loud via t.Fatalf. The returned slice is the union of every rule
// invocation's diagnostics; pass it to [Report] with a rule ID.
//
// Typed rules read pass.TypesInfo (and pass.Pkg) for resolution; AST-only
// helpers ([EachInSubtree] etc.) work unchanged on pass.Files.
func RunTyped(t *testing.T, opts TypedOpts, patterns []string, rule Rule) []Diagnostic {
	t.Helper()
	if rule == nil {
		t.Fatalf("archtest.RunTyped: nil rule")
	}
	if len(patterns) == 0 {
		t.Fatalf("archtest.RunTyped: at least one pattern required")
	}
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, opts.Tests, opts.Tags, patterns...)
	if err != nil {
		t.Fatalf("archtest.RunTyped: SharedResolver: %v", err)
	}

	seen := make(map[*ast.File]bool)
	var all []Diagnostic
	for _, pkg := range resolver.Packages() {
		pass := buildTypedPass(root, pkg, seen)
		if pass == nil {
			continue
		}
		all = append(all, rule(pass)...)
	}
	return all
}

// buildTypedPass returns a fully-populated typed *Pass for pkg, or nil when
// pkg lacks the required type info or contributes no new files (every file
// already consumed by an earlier pkg via seen). Extracted from [RunTyped]
// to keep the driver's cognitive complexity below the project's gocognit
// budget; behavior is unchanged.
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
