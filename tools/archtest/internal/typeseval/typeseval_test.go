package typeseval

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func buildFakePkg(t *testing.T, src string) (*packages.Package, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture source")

	info := &types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
	conf := types.Config{Importer: importer.Default()}
	typesPkg, err := conf.Check("fixture", fset, []*ast.File{file}, info)
	require.NoError(t, err, "type-check fixture source")

	return &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{file},
		TypesInfo: info,
		Types:     typesPkg,
	}, file
}

func firstCallArgs(t *testing.T, file *ast.File) []ast.Expr {
	t.Helper()
	var args []ast.Expr
	ast.Inspect(file, func(n ast.Node) bool {
		if args != nil {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			args = call.Args
			return false
		}
		return true
	})
	require.NotNil(t, args, "no call expression found in fixture")
	return args
}

func TestEvaluateConstString_BasicLitIdentBinaryExpr(t *testing.T) {
	src := `package fixture
import "fmt"
const SessionTopic = "session.created.v1"
func init() { fmt.Println(SessionTopic, "literal", SessionTopic + ".suffix") }
`
	pkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	require.Len(t, args, 3)

	v, ok := EvaluateConstString(pkg.TypesInfo, args[0])
	assert.True(t, ok)
	assert.Equal(t, "session.created.v1", v)

	v, ok = EvaluateConstString(pkg.TypesInfo, args[1])
	assert.True(t, ok)
	assert.Equal(t, "literal", v)

	v, ok = EvaluateConstString(pkg.TypesInfo, args[2])
	assert.True(t, ok)
	assert.Equal(t, "session.created.v1.suffix", v)
}

func TestEvaluateConstString_RejectsNonString(t *testing.T) {
	src := `package fixture
import "fmt"
const N = 42
func init() { fmt.Println(N) }
`
	pkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	_, ok := EvaluateConstString(pkg.TypesInfo, args[0])
	assert.False(t, ok)
}

func TestEvaluateConstString_RejectsNonConst(t *testing.T) {
	src := `package fixture
import "fmt"
var s string = "x"
func init() { fmt.Println(s) }
`
	pkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	_, ok := EvaluateConstString(pkg.TypesInfo, args[0])
	assert.False(t, ok)
}

func TestEvaluateConstString_NilTypesInfo(t *testing.T) {
	_, ok := EvaluateConstString(nil, &ast.BasicLit{})
	assert.False(t, ok)
}

func TestResolver_ResolveString(t *testing.T) {
	src := `package fixture
import "fmt"
const Topic = "x.y.z"
func init() { fmt.Println(Topic) }
`
	pkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	r := &Resolver{}
	v, ok := r.ResolveString(pkg, args[0])
	assert.True(t, ok)
	assert.Equal(t, "x.y.z", v)

	_, ok = r.ResolveString(nil, args[0])
	assert.False(t, ok, "nil pkg should not panic")
	_, ok = r.ResolveString(&packages.Package{TypesInfo: nil}, args[0])
	assert.False(t, ok, "nil TypesInfo should not panic")
}

func TestLoadPackages_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("packages.Load is slow under -short")
	}
	root := findArchTestModuleRoot(t)
	pkgs, errs, err := LoadPackages(root, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	require.Empty(t, errs, "load errors: %v", errs)
	require.NotEmpty(t, pkgs)

	var found bool
	for _, p := range pkgs {
		if p.Name == "typeseval" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected typeseval package in load result")
}

func TestLoadPackages_PropagatesErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("packages.Load is slow under -short")
	}
	root := findArchTestModuleRoot(t)
	_, errs, err := LoadPackages(root, "./tools/archtest/testdata/nonexistent/...")
	require.NoError(t, err, "loader itself should not error on missing pattern")
	assert.NotEmpty(t, errs, "missing pattern surfaces packages.Error entries via Visit")
}

func TestSharedResolver_Singleton(t *testing.T) {
	if testing.Short() {
		t.Skip("packages.Load is slow under -short")
	}
	root := findArchTestModuleRoot(t)
	r1, err := SharedResolver(root, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	r2, err := SharedResolver(root, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	assert.Same(t, r1, r2, "SharedResolver should return cached singleton for same key")
}

// findArchTestModuleRoot returns the absolute path of the gocell module root by
// walking up from the test source file location. typeseval has no dependency
// on the rest of archtest, so we cannot reuse findModuleRoot.
func findArchTestModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// thisFile = .../tools/archtest/internal/typeseval/typeseval_test.go → root = ../../../../
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
}
