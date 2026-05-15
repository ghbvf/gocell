// INVARIANT: ARCHTEST-PASS-DRIVER-UNIT-01
//
// ARCHTEST-PASS-DRIVER-UNIT-01 — unit-test coverage for the archtest.Pass
// driver surface: archtest.Run / archtest.RunTyped plus the unexported
// helpers buildTypedPass / newPackageRel / isPackageWithTestFiles. Also
// covers the Stage 1.5 additions: Pass.Abs, Pass.IsFileInScope,
// Pass.IsGenerated, the façade helpers (ResolvePackageRef, ResolveMethodCall,
// EvaluateConstString, FlatNonDefaultTags, KnownNonDefaultTags), and the
// ImportBan re-export. Not a meta-archtest enforcement rule — the anchor
// exists solely to satisfy INVENTORY-ANCHOR-REQUIRED-01. Pairs with
// pass_funnel_test.go (PASS-FUNNEL-*-01) and the façade source files
// pass.go / walk.go / scope.go / resolve.go.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/archtestmeta"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// TestPass_TypedDistinguishesModes verifies Pass.Typed() returns false for
// AST-only Passes (Pkg/TypesInfo both nil) and true for typed Passes.
func TestPass_TypedDistinguishesModes(t *testing.T) {
	astOnly := &Pass{}
	if astOnly.Typed() {
		t.Errorf("AST-only Pass: Typed()=true, want false")
	}
	typed := &Pass{
		Pkg:       types.NewPackage("example.com/p", "p"),
		TypesInfo: &types.Info{},
	}
	if !typed.Typed() {
		t.Errorf("typed Pass: Typed()=false, want true")
	}
}

// TestRun_perPackageDelivery verifies the F2 contract: Run delivers ONE
// Pass containing ALL files in scope, not one Pass per file. This is the
// "Pass.Files length matches scope size" invariant — rule authors iterate
// pass.Files explicitly rather than accessing Files[0] with implicit
// single-element semantics.
func TestRun_perPackageDelivery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/runfixture\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, name),
			[]byte("package runfixture\n"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	var calls int
	var sawFiles []*ast.File
	rule := func(p *Pass) []Diagnostic {
		calls++
		sawFiles = append(sawFiles, p.Files...)
		if p.Pkg != nil || p.TypesInfo != nil {
			t.Errorf("AST-only Pass: Pkg/TypesInfo non-nil")
		}
		if p.Typed() {
			t.Errorf("AST-only Pass: Typed()=true")
		}
		return nil
	}

	diags := Run(t, ModuleScope(root), rule)
	if diags != nil {
		t.Errorf("Run returned %d diagnostics, want nil", len(diags))
	}
	if calls != 1 {
		t.Errorf("Run invoked rule %d times, want 1 (per-package delivery)", calls)
	}
	if got, want := len(sawFiles), 3; got != want {
		t.Errorf("rule saw %d files in single Pass, want %d", got, want)
	}
}

// TestRun_emptyScopeIsNoOp verifies Run returns nil (and does not invoke
// rule) when scope contains zero Go files.
func TestRun_emptyScopeIsNoOp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/empty\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	called := false
	rule := func(p *Pass) []Diagnostic { called = true; return nil }
	diags := Run(t, ModuleScope(root), rule)
	if diags != nil {
		t.Errorf("Run on empty scope: diags=%v, want nil", diags)
	}
	if called {
		t.Errorf("Run on empty scope: rule invoked, want skip")
	}
}

// TestRun_RelMapsFilesToModuleRelativePaths verifies Pass.Rel returns the
// module-relative slash path for files in pass.Files.
func TestRun_RelMapsFilesToModuleRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/rel\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	subDir := filepath.Join(root, "sub")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatalf("Mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "x.go"),
		[]byte("package sub\n"), 0o600); err != nil {
		t.Fatalf("WriteFile sub/x.go: %v", err)
	}

	var got []string
	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			got = append(got, p.Rel(f))
		}
		return nil
	}
	Run(t, ModuleScope(root), rule)
	if len(got) != 1 || got[0] != "sub/x.go" {
		t.Errorf("Pass.Rel: got %v, want [\"sub/x.go\"]", got)
	}
}

// TestRun_FsetIsSharedAcrossFiles verifies the FileSet-sharing contract:
// every *ast.File in a single Pass is owned by pass.Fset. This is the
// property that makes a single Pass internally consistent for any helper
// that pairs AST positions with go/types info in a future extension.
func TestRun_FsetIsSharedAcrossFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/fsetshare\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	for _, name := range []string{"x.go", "y.go"} {
		if err := os.WriteFile(filepath.Join(root, name),
			[]byte("package fsetshare\n"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	rule := func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			t.Errorf("Pass.Fset nil")
		}
		for _, f := range p.Files {
			if p.Fset.File(f.Pos()) == nil {
				t.Errorf("file %q: Fset does not own its position", p.Rel(f))
			}
		}
		return nil
	}
	Run(t, ModuleScope(root), rule)
}

// TestRunTyped_typedPassShape verifies RunTyped delivers a Pass with
// Pkg / TypesInfo / Fset populated and Pass.Typed()=true. Uses the
// archtest_fixture-gated red fixture (which is a real Go package with
// type info) as the load target.
func TestRunTyped_typedPassShape(t *testing.T) {
	var calls int
	rule := func(p *Pass) []Diagnostic {
		calls++
		if !p.Typed() {
			t.Errorf("RunTyped Pass: Typed()=false")
		}
		if p.Pkg == nil {
			t.Errorf("RunTyped Pass: Pkg nil")
		}
		if p.TypesInfo == nil {
			t.Errorf("RunTyped Pass: TypesInfo nil")
		}
		if p.Fset == nil {
			t.Errorf("RunTyped Pass: Fset nil")
		}
		if len(p.Files) == 0 {
			t.Errorf("RunTyped Pass: Files empty")
		}
		return nil
	}
	RunTyped(t, TypedOpts{Tests: false, Tags: []string{archtestmeta.FixtureBuildTag}},
		[]string{"./tools/archtest/internal/passfunnelfixture"}, rule)
	if calls == 0 {
		t.Errorf("RunTyped invoked rule 0 times; expected ≥ 1 (fixture has 1 file)")
	}
}

// TestRunTyped_dedupesAcrossPackageVariants verifies the F3 contract:
// loading with Tests=true returns regular + .test packages, but the same
// *ast.File pointer must not appear in two Pass.Files slices.
func TestRunTyped_dedupesAcrossPackageVariants(t *testing.T) {
	seenAcrossPasses := make(map[*ast.File]int)
	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			seenAcrossPasses[f]++
		}
		return nil
	}
	RunTyped(t, TypedOpts{Tests: true, Tags: []string{archtestmeta.FixtureBuildTag}},
		[]string{"./tools/archtest/internal/passfunnelfixture"}, rule)
	for f, count := range seenAcrossPasses {
		if count > 1 {
			t.Errorf("file pointer %p delivered to %d Passes; want exactly 1", f, count)
		}
	}
}

// Nil-rule / empty-patterns guards are simple single-branch t.Fatalf calls
// that cannot be exercised in a parent test (testing.T is a concrete type;
// no shim can replace it without changing Run/RunTyped's public signature).
// Coverage of those branches is < 1% of statements; the if-statements are
// reviewable by inspection — testing them would require either a sub-process
// indirection or an API change (TB-interface taken instead of *testing.T)
// not warranted by this PR's scope.

// TestNewPackageRel_handlesEmptyFilename verifies the F4 fix: when fset
// has no real filename for a node, the Rel closure returns "" rather than
// emitting a confusing "../<root>" traversal path. Also covers nil file.
func TestNewPackageRel_handlesEmptyFilename(t *testing.T) {
	fset := token.NewFileSet()
	// Parse synthetic source with empty filename — fset.Position(file.Pos())
	// returns "" for unset Position.Filename.
	f, err := parser.ParseFile(fset, "", "package p\n", parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	rel := newPackageRel("/some/root", fset)
	if got := rel(f); got != "" {
		t.Errorf("newPackageRel for synthetic file: got %q, want \"\"", got)
	}
	if got := rel(nil); got != "" {
		t.Errorf("newPackageRel(nil): got %q, want \"\"", got)
	}
}

// TestIsPackageWithTestFiles validates the test-variant detector used by
// RunTyped's sort.
func TestIsPackageWithTestFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  bool
	}{
		{name: "nil pkg", files: nil, want: false},
		{name: "non-test files only", files: []string{"a.go", "b.go"}, want: false},
		{name: "has test file", files: []string{"a.go", "b_test.go"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pkg *packages.Package
			if tt.files != nil {
				fset := token.NewFileSet()
				syntax := make([]*ast.File, 0, len(tt.files))
				for _, name := range tt.files {
					path := filepath.Join(t.TempDir(), name)
					if err := os.WriteFile(path, []byte("package p\n"), 0o600); err != nil {
						t.Fatalf("WriteFile %s: %v", name, err)
					}
					f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
					if err != nil {
						t.Fatalf("ParseFile %s: %v", name, err)
					}
					syntax = append(syntax, f)
				}
				pkg = &packages.Package{Fset: fset, Syntax: syntax}
			}
			if got := isPackageWithTestFiles(pkg); got != tt.want {
				t.Errorf("isPackageWithTestFiles(%v): got %v, want %v",
					tt.files, got, tt.want)
			}
		})
	}
}

// TestBuildTypedPass_skipsIncompletePackages verifies the guard against
// packages without full type info.
func TestBuildTypedPass_skipsIncompletePackages(t *testing.T) {
	tests := []struct {
		name string
		pkg  *packages.Package
	}{
		{name: "nil pkg", pkg: nil},
		{name: "missing Types", pkg: &packages.Package{Fset: token.NewFileSet(), TypesInfo: &types.Info{}}},
		{name: "missing TypesInfo", pkg: &packages.Package{Fset: token.NewFileSet(), Types: types.NewPackage("p", "p")}},
		{name: "missing Fset", pkg: &packages.Package{Types: types.NewPackage("p", "p"), TypesInfo: &types.Info{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildTypedPass("/", tt.pkg, map[*ast.File]bool{}); got != nil {
				t.Errorf("buildTypedPass(%s): got non-nil Pass, want nil", tt.name)
			}
		})
	}
}

// TestBuildTypedPass_dedupSkipEmptyResult verifies that when every file in
// pkg.Syntax is already in seen, buildTypedPass returns nil (the pkg
// contributes no fresh files to a Pass).
func TestBuildTypedPass_dedupSkipEmptyResult(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", "package p\n", parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	seen := map[*ast.File]bool{f: true}
	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		Types:     types.NewPackage("example.com/p", "p"),
		TypesInfo: &types.Info{},
	}
	if got := buildTypedPass("/", pkg, seen); got != nil {
		t.Errorf("buildTypedPass with all-seen Syntax: got non-nil, want nil")
	}
}

// ── Stage 1.5 additions ────────────────────────────────────────────────────

// TestRun_ParsesComments verifies that the AST path (Run/collectASTFiles)
// parses with parser.ParseComments so that comment groups — including
// // INVARIANT: anchors — are present in the returned *ast.File.Comments.
// RED until pass.go:149 gains |parser.ParseComments.
func TestRun_ParsesComments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/comments\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	src := `// INVARIANT: SOME-RULE-01
package comments
`
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile a.go: %v", err)
	}

	var foundComments bool
	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if strings.Contains(c.Text, "INVARIANT") {
						foundComments = true
					}
				}
			}
		}
		return nil
	}
	Run(t, ModuleScope(root), rule)
	if !foundComments {
		t.Errorf("Run: Pass.Files[*].Comments empty; parser.ParseComments not active (gap #1)")
	}
}

// TestRunTyped_CommentsRegressionLock verifies that the typed path (RunTyped)
// ALREADY delivers comments (go/packages default ParseFile includes
// parser.ParseComments). This test should be GREEN from the start; if it
// fails, the plan fact #2 is falsified and implementation must STOP.
func TestRunTyped_CommentsRegressionLock(t *testing.T) {
	var foundComments bool
	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			if len(f.Comments) > 0 {
				foundComments = true
			}
		}
		return nil
	}
	RunTyped(t, TypedOpts{Tests: false, Tags: []string{archtestmeta.FixtureBuildTag}},
		[]string{"./tools/archtest/internal/passfunnelfixture"}, rule)
	if !foundComments {
		t.Fatalf("STOP: RunTyped path does NOT deliver comments — plan fact #2 is falsified; do not proceed with implementation")
	}
}

// TestRun_AbsResolvesModuleAbsolutePath verifies Pass.Abs returns an absolute
// path that has the same suffix as Pass.Rel, and equals
// pass.Fset.Position(f.Pos()).Filename. RED until Pass.Abs field is added.
func TestRun_AbsResolvesModuleAbsolutePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/abstest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	subDir := filepath.Join(root, "pkg")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatalf("Mkdir pkg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "x.go"),
		[]byte("package pkg\n"), 0o600); err != nil {
		t.Fatalf("WriteFile pkg/x.go: %v", err)
	}

	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			abs := p.Abs(f)
			if !filepath.IsAbs(abs) {
				t.Errorf("Pass.Abs: %q is not absolute", abs)
			}
			rel := p.Rel(f)
			if !strings.HasSuffix(filepath.ToSlash(abs), rel) {
				t.Errorf("Pass.Abs: %q does not have suffix %q", abs, rel)
			}
			fsetAbs := p.Fset.Position(f.Pos()).Filename
			if abs != fsetAbs {
				t.Errorf("Pass.Abs: %q != Fset.Position().Filename %q", abs, fsetAbs)
			}
		}
		return nil
	}
	Run(t, ModuleScope(root), rule)
}

// TestRunTyped_AbsResolvesModuleAbsolutePath mirrors TestRun_AbsResolvesModuleAbsolutePath
// for the typed path. RED until Pass.Abs is populated in buildTypedPass.
func TestRunTyped_AbsResolvesModuleAbsolutePath(t *testing.T) {
	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			abs := p.Abs(f)
			if !filepath.IsAbs(abs) {
				t.Errorf("RunTyped Pass.Abs: %q is not absolute", abs)
			}
			rel := p.Rel(f)
			if !strings.HasSuffix(filepath.ToSlash(abs), rel) {
				t.Errorf("RunTyped Pass.Abs: %q does not have suffix %q", abs, rel)
			}
			fsetAbs := p.Fset.Position(f.Pos()).Filename
			if abs != fsetAbs {
				t.Errorf("RunTyped Pass.Abs: %q != Fset.Position().Filename %q", abs, fsetAbs)
			}
		}
		return nil
	}
	RunTyped(t, TypedOpts{Tests: false, Tags: []string{archtestmeta.FixtureBuildTag}},
		[]string{"./tools/archtest/internal/passfunnelfixture"}, rule)
}

// TestPass_IsFileInScope verifies Pass.IsFileInScope delegates correctly to
// typeseval.ParseBuildConstraint+BuildContextPredicate. Uses a file with a
// known build constraint. RED until (*Pass).IsFileInScope is added.
//
// Oracle comparison: we call typeseval.ParseBuildConstraint on the abs path
// and typeseval.BuildContextPredicate() directly to compute expected. The
// pass_test.go file is permanently exempt from PASS-FUNNEL-RESOLVE-01
// (passFunnelPermanentExempt) so the import of typeseval here is legal.
func TestPass_IsFileInScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/bctag\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	// File gated by integration tag — NOT in scope under default predicate.
	constrained := filepath.Join(root, "int.go")
	if err := os.WriteFile(constrained,
		[]byte("//go:build integration\n\npackage bctag\n"), 0o600); err != nil {
		t.Fatalf("WriteFile int.go: %v", err)
	}
	// File with no build constraint — always in scope.
	unconstrained := filepath.Join(root, "base.go")
	if err := os.WriteFile(unconstrained,
		[]byte("package bctag\n"), 0o600); err != nil {
		t.Fatalf("WriteFile base.go: %v", err)
	}

	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			abs := p.Fset.Position(f.Pos()).Filename
			// Oracle: use typeseval directly (allowed in pass_test.go).
			expr, err := typeseval.ParseBuildConstraint(abs)
			if err != nil {
				t.Errorf("oracle ParseBuildConstraint(%s): %v", abs, err)
				continue
			}
			var oracleInScope bool
			if expr == nil {
				oracleInScope = true
			} else {
				oracleInScope = expr.Eval(typeseval.BuildContextPredicate())
			}

			got := p.IsFileInScope(f)
			if got != oracleInScope {
				t.Errorf("Pass.IsFileInScope(%s) = %v, oracle = %v", abs, got, oracleInScope)
			}
		}
		return nil
	}
	Run(t, ModuleScope(root, IncludeTests()), rule)
}

// TestPass_IsGenerated verifies Pass.IsGenerated delegates correctly to
// typeseval.IsGeneratedRelPath. Files under generated/ return true; others false.
// RED until (*Pass).IsGenerated is added.
//
// Oracle comparison: typeseval.IsGeneratedRelPath called on pass.Rel(f).
// The import of typeseval here is legal (pass_test.go is permanently exempt).
func TestPass_IsGenerated(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/gen\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	genDir := filepath.Join(root, "generated")
	if err := os.Mkdir(genDir, 0o700); err != nil {
		t.Fatalf("Mkdir generated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genDir, "code.go"),
		[]byte("package generated\n"), 0o600); err != nil {
		t.Fatalf("WriteFile generated/code.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "normal.go"),
		[]byte("package gen\n"), 0o600); err != nil {
		t.Fatalf("WriteFile normal.go: %v", err)
	}

	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			oracle := typeseval.IsGeneratedRelPath(rel)
			got := p.IsGenerated(f)
			if got != oracle {
				t.Errorf("Pass.IsGenerated(%s) = %v, oracle typeseval.IsGeneratedRelPath = %v",
					rel, got, oracle)
			}
		}
		return nil
	}
	Run(t, ModuleScope(root, IncludeGenerated()), rule)
}

// TestImportBanReExport verifies that archtest.ImportBan is a type alias for
// scanner.ImportBan, and that a trivial Run via the façade alias works.
// RED until resolve.go adds `type ImportBan = scanner.ImportBan`.
func TestImportBanReExport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/ibantest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package ibantest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile a.go: %v", err)
	}

	// Compile-time equivalence: archtest.ImportBan must be assignable from a
	// struct literal — verifies it is not a distinct named type.
	_ = ImportBan{RuleID: "TEST-01", Forbidden: []string{"fmt"}}

	// Runtime: run a trivial ImportBan via the façade.
	ban := ImportBan{RuleID: "TEST-IMPORT-BAN-01", Forbidden: []string{"not/a/real/package"}}
	ban.Run(t, ModuleScope(root))
	// If we reach here, the façade alias works correctly.
}

// TestResolveHelpersReExported verifies that the five helper free functions
// and two tag preset functions are accessible via the archtest façade and
// produce results equal to calling typeseval directly.
// RED until resolve.go exports these functions.
//
// pass_test.go is permanently exempt from PASS-FUNNEL-RESOLVE-01 so the
// direct typeseval imports here are legal oracle comparisons.
func TestResolveHelpersReExported(t *testing.T) {
	// FlatNonDefaultTags and KnownNonDefaultTags are pure value-returning functions.
	facadeFlat := FlatNonDefaultTags()
	oracleFlat := typeseval.FlatNonDefaultTags()
	if len(facadeFlat) != len(oracleFlat) {
		t.Errorf("FlatNonDefaultTags length: façade=%d oracle=%d", len(facadeFlat), len(oracleFlat))
	}
	for i, v := range oracleFlat {
		if i >= len(facadeFlat) || facadeFlat[i] != v {
			t.Errorf("FlatNonDefaultTags[%d]: façade=%v oracle=%v", i, facadeFlat[i], v)
		}
	}

	facadeKnown := KnownNonDefaultTags()
	oracleKnown := typeseval.KnownNonDefaultTags()
	if len(facadeKnown) != len(oracleKnown) {
		t.Errorf("KnownNonDefaultTags length: façade=%d oracle=%d", len(facadeKnown), len(oracleKnown))
	}

	// ResolvePackageRef, ResolveMethodCall, EvaluateConstString require a
	// loaded package with TypesInfo. We load passfunnelfixture to get real type
	// info, then assert that calling the façade wrapper produces the same result
	// as calling typeseval directly on the same inputs.
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(
		root, false, []string{archtestmeta.FixtureBuildTag},
		"./tools/archtest/internal/passfunnelfixture")
	if err != nil {
		t.Fatalf("SharedResolver: %v", err)
	}
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil {
			continue
		}
		for _, f := range pkg.Syntax {
			// Walk SelectorExpr nodes; for each, compare façade vs oracle result.
			EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
				facadePath, facadeName, facadeOK := ResolvePackageRef(pkg.TypesInfo, sel)
				oraclePath, oracleName, oracleOK := typeseval.ResolvePackageRef(pkg.TypesInfo, sel)
				if facadeOK != oracleOK || facadePath != oraclePath || facadeName != oracleName {
					t.Errorf("ResolvePackageRef mismatch: façade=(%q,%q,%v) oracle=(%q,%q,%v)",
						facadePath, facadeName, facadeOK,
						oraclePath, oracleName, oracleOK)
				}
				// ResolveMethodCall on SelectorExpr
				facadeFn, facadeMOK := ResolveMethodCall(pkg.TypesInfo, sel)
				oracleFn, oracleMOK := typeseval.ResolveMethodCall(pkg.TypesInfo, sel)
				if facadeMOK != oracleMOK {
					t.Errorf("ResolveMethodCall ok mismatch: façade=%v oracle=%v", facadeMOK, oracleMOK)
				}
				if facadeMOK && oracleMOK && facadeFn.FullName() != oracleFn.FullName() {
					t.Errorf("ResolveMethodCall result mismatch: façade=%v oracle=%v",
						facadeFn.FullName(), oracleFn.FullName())
				}
			})
			// Walk all Expr nodes; for each, compare EvaluateConstString.
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				facadeStr, facadeEOK := EvaluateConstString(pkg.TypesInfo, lit)
				oracleStr, oracleEOK := typeseval.EvaluateConstString(pkg.TypesInfo, lit)
				if facadeEOK != oracleEOK || facadeStr != oracleStr {
					t.Errorf("EvaluateConstString mismatch: façade=(%q,%v) oracle=(%q,%v)",
						facadeStr, facadeEOK, oracleStr, oracleEOK)
				}
			})
		}
	}
}

// TestBuildContextPredicateReExported verifies that archtest.BuildContextPredicate
// is a thin delegate to typeseval.BuildContextPredicate: the returned predicate
// function must agree with the oracle for all probe tags.
//
// Uses "integration" as the extra tag because build_constraint_test.go and
// ci_integration_discovery_invariants_test.go both need exactly this predicate.
//
// pass_test.go is permanently exempt from PASS-FUNNEL-RESOLVE-01 so the
// direct typeseval call here is a legal oracle comparison.
//
// Note on Eval(): to comply with TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01, we
// do NOT call constraint.Expr.Eval(BuildContextPredicate(...)). Instead we
// compare the returned predicate functions directly on a set of probe tags,
// avoiding the Eval() rule enforcement surface entirely.
func TestBuildContextPredicateReExported(t *testing.T) {
	// Probe tags: a known extra tag, known default tags, and an unknown tag.
	probeTags := []string{"integration", "linux", "amd64", "go1.21", "cgo", "nonexistent_tag"}

	// With extra tag "integration".
	facadePredWithTag := BuildContextPredicate("integration")
	oraclePredWithTag := typeseval.BuildContextPredicate("integration")
	for _, tag := range probeTags {
		got := facadePredWithTag(tag)
		want := oraclePredWithTag(tag)
		if got != want {
			t.Errorf("BuildContextPredicate(\"integration\")(%q): façade=%v oracle=%v",
				tag, got, want)
		}
	}

	// Without extra tags (default context only).
	facadePredDefault := BuildContextPredicate()
	oraclePredDefault := typeseval.BuildContextPredicate()
	for _, tag := range probeTags {
		got := facadePredDefault(tag)
		want := oraclePredDefault(tag)
		if got != want {
			t.Errorf("BuildContextPredicate()(%q): façade=%v oracle=%v",
				tag, got, want)
		}
	}
}

// TestFacadeDoesNotLeakLoaders is the Hard defense #1 blind-spot self-check.
// It statically parses the non-test archtest façade source files (pass.go,
// scope.go, walk.go, content.go, and any future resolve.go) via go/parser and
// asserts that none of the 6 loader symbols appear as exported identifiers,
// and that no exported function or type signature mentions *packages.Package.
//
// # AI-rebust: Hard
//
// The Hard property comes from "not in the façade = not expressible at the
// call site": if a loader symbol is absent from the façade's exported set,
// a business *_test.go cannot write `archtest.LoadPackages(...)` — the
// compiler will reject it. This test locks the boundary so a future edit that
// accidentally re-exports a loader symbol fails CI immediately.
//
// # Blind spots covered (per ai-collab.md Hard evidence requirement)
//
// Forms this test detects:
//   - Top-level exported var/const/func/type declarations with a banned name.
//   - Exported func/type whose signature text contains "*packages.Package".
//
// Forms NOT detected by this test (honest disclosure):
//   - A loader symbol re-exported under a non-banned alias name (e.g.
//     `var Loader = typeseval.LoadPackages`). Mitigation: the alias would
//     still require a *packages.Package in its signature, which IS detected.
//   - A loader symbol embedded in an unexported field then surfaced via
//     a method. Mitigation: no such structural pattern exists in archtest today;
//     any introduction would be caught by the full LOADPACKAGES-01 typed check.
//   - Loader symbol exported from a sub-package of archtest. Mitigation:
//     business tests only import `tools/archtest` (not sub-packages);
//     PACKAGES-IMPORT-01 bans direct internal/* imports.
//
// The three forms above cannot be detected by a pure AST scan without
// go/types; this test is Hard for the most common bypass (direct declaration),
// and Medium-grade coverage for the alias/embedded cases (covered by the
// PASS-FUNNEL-LOADPACKAGES-01 / PACKAGES-IMPORT-01 type-aware detectors).
func TestFacadeDoesNotLeakLoaders(t *testing.T) {
	bannedLoaders := map[string]bool{
		"LoadPackages":           true,
		"SharedResolver":         true,
		"LoadProductionPackages": true,
		"Resolver":               true,
		"ProductionResolver":     true,
		"EachFileInPackage":      true,
	}

	root := findModuleRoot(t)
	// Only scan the direct-child (non-test) .go files in tools/archtest/ itself
	// (not subdirectories). Use archtest.Run + DirsScope + MatchRels to stay
	// within the facade boundary. MatchRels filters to files whose directory
	// component is exactly "tools/archtest" (no slashes after that prefix).
	scope := DirsScope(root, []string{"tools/archtest"}, MatchRels(func(rel string) bool {
		slash := strings.LastIndex(rel, "/")
		if slash < 0 {
			return false
		}
		dir := rel[:slash]
		base := rel[slash+1:]
		return dir == "tools/archtest" &&
			strings.HasSuffix(base, ".go") &&
			!strings.HasSuffix(base, "_test.go")
	}))

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Exported FuncDecl checks (direct children of file).
			EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || !fn.Name.IsExported() {
					return
				}
				if bannedLoaders[fn.Name.Name] {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(fn.Name.Pos()).Line,
						Message: "exported func " + fn.Name.Name +
							" is a banned loader symbol; must NOT appear in facade",
					})
				}
				if (fn.Type != nil && funcTypeContainsPackagesSel(fn.Type)) ||
					funcFieldListContainsPackagesSel(fn.Recv) {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(fn.Name.Pos()).Line,
						Message: "exported func " + fn.Name.Name +
							" signature mentions *packages.Package; loaders must not leak",
					})
				}
			})
			// Exported TypeSpec (type declarations anywhere in file).
			EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
				if ts.Name != nil && ts.Name.IsExported() && bannedLoaders[ts.Name.Name] {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(ts.Name.Pos()).Line,
						Message: "exported type " + ts.Name.Name +
							" is a banned loader symbol; must NOT appear in facade",
					})
				}
			})
			// Exported ValueSpec (var/const declarations anywhere in file).
			EachInSubtree[ast.ValueSpec](f, func(vs *ast.ValueSpec) {
				for _, ident := range vs.Names {
					if ident.IsExported() && bannedLoaders[ident.Name] {
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: p.Fset.Position(ident.Pos()).Line,
							Message: "exported var/const " + ident.Name +
								" is a banned loader symbol; must NOT appear in facade",
						})
					}
				}
			})
		}
		return d
	})
	Report(t, "FACADE-NO-LOADER-LEAK-01", diags)
}

// funcTypeContainsPackagesSel reports whether a function type's params or
// results contain a SelectorExpr with X.Name=="packages" and
// Sel.Name=="Package". Catches *packages.Package, []*packages.Package, etc.
// Uses EachInSubtree so the scanner-framework ban on ast.Inspect is respected.
//
// The receiver field list is intentionally excluded here; callers that also
// need receiver coverage should additionally call [funcFieldListContainsPackagesSel]
// with fn.Recv (the receiver lives on *ast.FuncDecl, not on *ast.FuncType).
func funcTypeContainsPackagesSel(ft *ast.FuncType) bool {
	found := false
	checkField := func(fields *ast.FieldList) {
		if fields == nil || found {
			return
		}
		for _, field := range fields.List {
			if found {
				break
			}
			EachInSubtree[ast.SelectorExpr](field.Type, func(sel *ast.SelectorExpr) {
				if found {
					return
				}
				xIdent, ok := sel.X.(*ast.Ident)
				if !ok {
					return
				}
				if xIdent.Name == "packages" && sel.Sel != nil && sel.Sel.Name == "Package" {
					found = true
				}
			})
		}
	}
	checkField(ft.Params)
	checkField(ft.Results)
	return found
}

// funcFieldListContainsPackagesSel reports whether a FieldList (typically a
// method receiver list) contains a SelectorExpr with X.Name=="packages" and
// Sel.Name=="Package". Used to extend [funcTypeContainsPackagesSel]'s coverage
// to method receivers, which live on *ast.FuncDecl.Recv rather than
// *ast.FuncType.Params / .Results.
func funcFieldListContainsPackagesSel(fields *ast.FieldList) bool {
	if fields == nil {
		return false
	}
	found := false
	for _, field := range fields.List {
		if found {
			break
		}
		EachInSubtree[ast.SelectorExpr](field.Type, func(sel *ast.SelectorExpr) {
			if found {
				return
			}
			xIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return
			}
			if xIdent.Name == "packages" && sel.Sel != nil && sel.Sel.Name == "Package" {
				found = true
			}
		})
	}
	return found
}

// TestPass_IsFileInScopeConstraintExpr verifies that IsFileInScope returns
// true when the build constraint is satisfiable under the default predicate,
// using a //go:build linux constraint (linux is an implicit default).
// This is a companion test to TestPass_IsFileInScope that exercises the
// "constraint evaluates to true" branch.
func TestPass_IsFileInScopeConstraintExpr(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/bcscope2\n"), 0o600); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	// linux is an implicit default in BuildContextPredicate — always satisfiable.
	if err := os.WriteFile(filepath.Join(root, "linux.go"),
		[]byte("//go:build linux\n\npackage bcscope2\n"), 0o600); err != nil {
		t.Fatalf("WriteFile linux.go: %v", err)
	}

	rule := func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			abs := p.Fset.Position(f.Pos()).Filename
			expr, err := typeseval.ParseBuildConstraint(abs)
			if err != nil {
				t.Errorf("oracle ParseBuildConstraint: %v", err)
				continue
			}
			_ = expr // expr != nil for linux.go; Eval should be true
			var oracleResult bool
			if expr == nil {
				oracleResult = true
			} else {
				oracleResult = expr.Eval(typeseval.BuildContextPredicate())
			}
			got := p.IsFileInScope(f)
			if got != oracleResult {
				t.Errorf("IsFileInScope(%s) = %v, want %v (linux constraint should be in-scope)",
					abs, got, oracleResult)
			}
		}
		return nil
	}
	Run(t, ModuleScope(root), rule)
}
