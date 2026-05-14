package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/archtestmeta"
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
