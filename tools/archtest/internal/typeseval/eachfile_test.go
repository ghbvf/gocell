package typeseval

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/packages"
)

func TestEachFileInPackage_NilGuards(t *testing.T) {
	called := 0
	cb := func(_ *ast.File, _ string, _ *types.Info, _ *token.FileSet) { called++ }

	EachFileInPackage("/root", nil, false, cb)
	assert.Equal(t, 0, called, "nil pkg must be a no-op")

	EachFileInPackage("/root", &packages.Package{}, false, cb)
	assert.Equal(t, 0, called, "pkg without TypesInfo must be a no-op")
}

func TestEachFileInPackage_PairsSyntaxWithGoFiles(t *testing.T) {
	pkg := &packages.Package{
		Syntax:    []*ast.File{{}, {}, {}},
		GoFiles:   []string{"/root/a.go", "/root/b_test.go", "/root/c.go"},
		TypesInfo: &types.Info{},
		Fset:      token.NewFileSet(),
	}

	var seen []string
	EachFileInPackage("/root", pkg, false, func(_ *ast.File, rel string, _ *types.Info, _ *token.FileSet) {
		seen = append(seen, rel)
	})
	assert.Equal(t, []string{"a.go", "b_test.go", "c.go"}, seen,
		"all files iterated when skipTestFiles=false")
}

func TestEachFileInPackage_SkipsTestFiles(t *testing.T) {
	pkg := &packages.Package{
		Syntax:    []*ast.File{{}, {}, {}},
		GoFiles:   []string{"/root/a.go", "/root/b_test.go", "/root/c.go"},
		TypesInfo: &types.Info{},
		Fset:      token.NewFileSet(),
	}

	var seen []string
	EachFileInPackage("/root", pkg, true, func(_ *ast.File, rel string, _ *types.Info, _ *token.FileSet) {
		seen = append(seen, rel)
	})
	assert.Equal(t, []string{"a.go", "c.go"}, seen,
		"_test.go files filtered when skipTestFiles=true")
}

func TestEachFileInPackage_LengthMismatchSkipsExtraSyntax(t *testing.T) {
	pkg := &packages.Package{
		Syntax:    []*ast.File{{}, {}, {}},
		GoFiles:   []string{"/root/a.go"},
		TypesInfo: &types.Info{},
		Fset:      token.NewFileSet(),
	}

	var seen []string
	EachFileInPackage("/root", pkg, false, func(_ *ast.File, rel string, _ *types.Info, _ *token.FileSet) {
		seen = append(seen, rel)
	})
	assert.Equal(t, []string{"a.go"}, seen,
		"Syntax entries past len(GoFiles) must be skipped — fail-closed")
}
