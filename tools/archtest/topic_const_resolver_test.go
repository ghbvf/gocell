package archtest

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// buildFakePkg compiles src in-process using go/types and wraps the resulting
// TypesInfo in a packages.Package stub. This avoids packages.Load (which needs
// a real module on disk) while still exercising ResolveString through the same
// TypesInfo path used in production.
func buildFakePkg(t *testing.T, src string) (*packages.Package, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture source")

	info := &types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
	conf := types.Config{Importer: importer.Default()}
	typesPkg, err := conf.Check("fixture", fset, []*ast.File{file}, info)
	require.NoError(t, err, "type-check fixture source")

	fakePkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{file},
		TypesInfo: info,
		Types:     typesPkg,
	}
	return fakePkg, file
}

// firstCallArgs finds the first *ast.CallExpr in the file and returns its
// argument slice. Used by tests that embed test values as function arguments.
func firstCallArgs(t *testing.T, file *ast.File) []ast.Expr {
	t.Helper()
	var args []ast.Expr
	ast.Inspect(file, func(n ast.Node) bool {
		if args != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if ok {
			args = call.Args
			return false
		}
		return true
	})
	require.NotNil(t, args, "no call expression found in fixture")
	return args
}

// TestResolveString_HandlesBasicLitIdentSelector covers the three expression
// kinds that the production scanner encounters:
//   - Ident referencing a package-level const → folded by go/types
//   - BasicLit string literal → folded by go/types
//   - BinaryExpr (concat of const + literal) → folded by go/types
func TestResolveString_HandlesBasicLitIdentSelector(t *testing.T) {
	src := `package fixture
import "fmt"
const SessionTopic = "session.created.v1"
func init() { fmt.Println(SessionTopic, "literal", SessionTopic + ".suffix") }
`
	fakePkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	require.Len(t, args, 3)

	r := &topicConstResolver{}

	// Ident → const value
	v, ok := r.ResolveString(fakePkg, args[0])
	assert.True(t, ok, "Ident referring to const should resolve")
	assert.Equal(t, "session.created.v1", v)

	// BasicLit → literal value
	v, ok = r.ResolveString(fakePkg, args[1])
	assert.True(t, ok, "BasicLit should resolve")
	assert.Equal(t, "literal", v)

	// BinaryExpr → go/types folds to "session.created.v1.suffix"
	v, ok = r.ResolveString(fakePkg, args[2])
	assert.True(t, ok, "BinaryExpr of const + literal should resolve")
	assert.Equal(t, "session.created.v1.suffix", v)
}

// TestResolveString_RejectsNonString verifies that integer constants return
// ok=false.
func TestResolveString_RejectsNonString(t *testing.T) {
	src := `package fixture
import "fmt"
const N = 42
func init() { fmt.Println(N) }
`
	fakePkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	require.Len(t, args, 1)

	r := &topicConstResolver{}
	_, ok := r.ResolveString(fakePkg, args[0])
	assert.False(t, ok, "integer const should not resolve as string")
}

// TestResolveString_RejectsNonConst verifies that runtime variables (not
// compile-time constants) return ok=false.
func TestResolveString_RejectsNonConst(t *testing.T) {
	src := `package fixture
import "fmt"
var s string = "x"
func init() { fmt.Println(s) }
`
	fakePkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)
	require.Len(t, args, 1)

	r := &topicConstResolver{}
	_, ok := r.ResolveString(fakePkg, args[0])
	assert.False(t, ok, "runtime variable should not resolve as const string")
}

// TestResolveString_NilPkg ensures no panic when pkg or TypesInfo is nil.
func TestResolveString_NilPkg(t *testing.T) {
	r := &topicConstResolver{}
	// nil package
	_, ok := r.ResolveString(nil, &ast.BasicLit{})
	assert.False(t, ok)
	// nil TypesInfo
	_, ok = r.ResolveString(&packages.Package{TypesInfo: nil}, &ast.BasicLit{})
	assert.False(t, ok)
}
