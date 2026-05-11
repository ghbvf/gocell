package typesutil

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFakePkg parses + type-checks src in-memory. Populates the full
// *types.Info maps that ResolveReceiverType depends on (Types, Uses,
// Defs, Selections).
func buildFakePkg(t *testing.T, src string) *types.Info {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture source")

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Instances:  make(map[*ast.Ident]types.Instance),
	}
	conf := types.Config{Importer: importer.Default()}
	_, err = conf.Check("fixture", fset, []*ast.File{file}, info)
	require.NoError(t, err, "type-check fixture source")
	return info
}

// firstCall parses + type-checks src and returns the first *ast.CallExpr
// alongside the populated *types.Info.
func firstCall(t *testing.T, src string) (*ast.CallExpr, *types.Info) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture source")

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Instances:  make(map[*ast.Ident]types.Instance),
	}
	conf := types.Config{Importer: importer.Default()}
	_, err = conf.Check("fixture", fset, []*ast.File{file}, info)
	require.NoError(t, err, "type-check fixture source")

	var out *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		if out != nil {
			return false
		}
		if c, ok := node.(*ast.CallExpr); ok {
			out = c
			return false
		}
		return true
	})
	require.NotNil(t, out, "no call expression in fixture")
	return out, info
}

func TestResolveReceiverType_PointerMethod(t *testing.T) {
	src := `package fixture
type T struct{}
func (t *T) M() {}
func init() { (&T{}).M() }
`
	call, info := firstCall(t, src)
	named, isPtr, ok := ResolveReceiverType(info, call)
	require.True(t, ok)
	assert.Equal(t, "T", named.Obj().Name())
	assert.True(t, isPtr)
}

func TestResolveReceiverType_ValueMethod(t *testing.T) {
	src := `package fixture
type T struct{}
func (t T) M() {}
func init() { T{}.M() }
`
	call, info := firstCall(t, src)
	named, isPtr, ok := ResolveReceiverType(info, call)
	require.True(t, ok)
	assert.Equal(t, "T", named.Obj().Name())
	assert.False(t, isPtr)
}

func TestResolveReceiverType_PackageLevelFunc(t *testing.T) {
	src := `package fixture
func F() {}
func init() { F() }
`
	call, info := firstCall(t, src)
	named, _, ok := ResolveReceiverType(info, call)
	assert.False(t, ok)
	assert.Nil(t, named)
}

func TestResolveReceiverType_Builtin(t *testing.T) {
	src := `package fixture
func init() { _ = len("x") }
`
	call, info := firstCall(t, src)
	named, _, ok := ResolveReceiverType(info, call)
	assert.False(t, ok, "builtin len should not resolve to named receiver")
	assert.Nil(t, named)
}

func TestResolveReceiverType_InterfaceDispatch(t *testing.T) {
	src := `package fixture
type I interface { M() }
func use(i I) { i.M() }
`
	// The first call in source order is i.M() inside use().
	call, info := firstCall(t, src)
	named, _, ok := ResolveReceiverType(info, call)
	assert.False(t, ok, "interface dispatch should not resolve to a concrete named type")
	assert.Nil(t, named)
}

func TestResolveReceiverType_MethodValue(t *testing.T) {
	src := `package fixture
type T struct{}
func (t *T) M() {}
func init() {
	m := (&T{}).M
	m()
}
`
	// Two calls: (&T{}).M (method value expression — not a call by itself
	// since it's not invoked here), and m() at the end. We want m().
	// In AST the first CallExpr is m() (the only invocation).
	call, info := firstCall(t, src)
	named, _, ok := ResolveReceiverType(info, call)
	assert.False(t, ok, "method value invocation should not resolve via StaticCallee")
	assert.Nil(t, named)
}

func TestResolveReceiverType_PromotedEmbeddedMethod(t *testing.T) {
	src := `package fixture
type Base struct{}
func (b *Base) M() {}
type Outer struct { *Base }
func init() { (&Outer{Base: &Base{}}).M() }
`
	call, info := firstCall(t, src)
	named, isPtr, ok := ResolveReceiverType(info, call)
	require.True(t, ok)
	assert.Equal(t, "Base", named.Obj().Name(),
		"promoted method's receiver is the embedded type, not the outer")
	assert.True(t, isPtr)
}

func TestResolveReceiverType_GenericMethod(t *testing.T) {
	src := `package fixture
type Box[T any] struct { v T }
func (b Box[T]) Get() T { return b.v }
func init() { _ = Box[int]{}.Get() }
`
	call, info := firstCall(t, src)
	named, isPtr, ok := ResolveReceiverType(info, call)
	require.True(t, ok)
	assert.Equal(t, "Box", named.Obj().Name(),
		"generic method resolves to the generic base type, not the instantiation")
	assert.False(t, isPtr)
}

func TestResolveReceiverType_NilTypesInfo(t *testing.T) {
	call := &ast.CallExpr{}
	named, isPtr, ok := ResolveReceiverType(nil, call)
	assert.False(t, ok)
	assert.False(t, isPtr)
	assert.Nil(t, named)
}

func TestResolveReceiverType_NilCall(t *testing.T) {
	info := buildFakePkg(t, `package fixture`)
	named, isPtr, ok := ResolveReceiverType(info, nil)
	assert.False(t, ok)
	assert.False(t, isPtr)
	assert.Nil(t, named)
}
