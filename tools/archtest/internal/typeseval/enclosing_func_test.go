package typeseval

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findFirstCall returns the first ast.CallExpr in file. Used as the "node"
// argument for ResolveEnclosingFunc lookups in test fixtures.
func findFirstCall(t *testing.T, file *ast.File) *ast.CallExpr {
	t.Helper()
	var found *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			found = call
			return false
		}
		return true
	})
	require.NotNil(t, found, "no CallExpr found in fixture")
	return found
}

func TestResolveEnclosingFunc_TopLevelFunc(t *testing.T) {
	src := `package fixture
import "fmt"
func DoThing() { fmt.Println("hi") }
`
	pkg, file := buildFakePkg(t, src)
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, call)
	require.True(t, ok, "top-level func enclosing must resolve")
	assert.Equal(t, "DoThing", fn.Name())
	assert.Equal(t, "fixture", fn.Pkg().Path())
	assert.Equal(t, "fixture.DoThing", fn.FullName())
}

func TestResolveEnclosingFunc_PointerMethod(t *testing.T) {
	src := `package fixture
import "fmt"
type Service struct{}
func (s *Service) Run() { fmt.Println("run") }
`
	pkg, file := buildFakePkg(t, src)
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, call)
	require.True(t, ok, "pointer-receiver method enclosing must resolve")
	assert.Equal(t, "Run", fn.Name())
	assert.Equal(t, "(*fixture.Service).Run", fn.FullName(),
		"go/types canonical FullName form for pointer receiver")
}

func TestResolveEnclosingFunc_ValueMethod(t *testing.T) {
	src := `package fixture
import "fmt"
type Counter struct{}
func (c Counter) Inc() { fmt.Println("inc") }
`
	pkg, file := buildFakePkg(t, src)
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, call)
	require.True(t, ok, "value-receiver method enclosing must resolve")
	assert.Equal(t, "Inc", fn.Name())
	assert.Equal(t, "(fixture.Counter).Inc", fn.FullName(),
		"go/types canonical FullName form for value receiver")
}

func TestResolveEnclosingFunc_FuncLitInsideFuncDecl_ReturnsOuter(t *testing.T) {
	// Deliberate semantic choice: nested FuncLit inherits outer FuncDecl
	// identity. Allowlisting the outer FuncDecl implicitly trusts any FuncLit
	// inside it (FuncLit author = FuncDecl author).
	src := `package fixture
import "fmt"
func OuterHandler() {
	fn := func() { fmt.Println("inside literal") }
	fn()
}
`
	pkg, file := buildFakePkg(t, src)
	// We want the Println call (inside the FuncLit), not the fn() call.
	var target *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if target != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Println" {
			target = call
			return false
		}
		return true
	})
	require.NotNil(t, target, "could not find Println call in fixture")

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, target)
	require.True(t, ok, "FuncLit-nested call must resolve to outer FuncDecl")
	assert.Equal(t, "OuterHandler", fn.Name(),
		"identity must be outer FuncDecl, not anonymous FuncLit")
}

func TestResolveEnclosingFunc_PackageLevelVarInit_ReturnsFalse(t *testing.T) {
	// var-init with FuncLit at package scope has no enclosing FuncDecl.
	// Callers treat (nil, false) as an automatic violation — see archtest
	// godoc for the var-init blind-spot reverse self-check.
	src := `package fixture
import "fmt"
var _ = func() int { fmt.Println("init-time"); return 0 }()
`
	pkg, file := buildFakePkg(t, src)
	// Find the Println call (not the outer FuncLit invocation, which is also a CallExpr).
	var target *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if target != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Println" {
			target = call
			return false
		}
		return true
	})
	require.NotNil(t, target, "could not find Println call in fixture")

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, target)
	assert.False(t, ok, "package-level var init must not resolve to a FuncDecl")
	assert.Nil(t, fn)
}

func TestResolveEnclosingFunc_PackageLevelConstInit_ReturnsFalse(t *testing.T) {
	// Package-level const init with a call inside an expression — same
	// blind-spot category as var-init. Returns (nil, false).
	src := `package fixture
func helper() int { return 7 }
var X = helper()
`
	pkg, file := buildFakePkg(t, src)
	// First call in source order is helper() — but it's inside var X init,
	// not inside any FuncDecl.
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, call)
	assert.False(t, ok, "var X = helper() at package scope must not resolve to a FuncDecl")
	assert.Nil(t, fn)
}

func TestResolveEnclosingFunc_InitFunc(t *testing.T) {
	// Go's init() is a FuncDecl (named "init"); it must resolve like any
	// other FuncDecl. Multiple init() per file are distinct FuncDecls — each
	// resolves to its own *types.Func identity (Go assigns synthetic numeric
	// suffixes internally for collision tracking, but the FuncDecl.Name.Name
	// is always "init").
	src := `package fixture
import "fmt"
func init() { fmt.Println("init") }
`
	pkg, file := buildFakePkg(t, src)
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, call)
	require.True(t, ok, "init() FuncDecl must resolve")
	assert.Equal(t, "init", fn.Name())
}

func TestResolveEnclosingFunc_GenericReceiverMethod(t *testing.T) {
	// Generic receivers resolve normally; FullName encodes type parameters
	// per go/types canonical form.
	src := `package fixture
import "fmt"
type Container[T any] struct{ v T }
func (c *Container[T]) Show() { fmt.Println(c.v) }
`
	pkg, file := buildFakePkg(t, src)
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, call)
	require.True(t, ok, "generic-receiver method must resolve")
	assert.Equal(t, "Show", fn.Name())
}

func TestResolveEnclosingFunc_FuncLitInStructLiteral_ReturnsOuter(t *testing.T) {
	// A FuncLit nested inside a struct literal (e.g. http.HandlerFunc(func() {...})
	// assigned to a struct field) is the same lexical-containment case as a
	// FuncLit assigned to a local variable: position is inside the outer
	// FuncDecl, identity collapses to the outer FuncDecl.
	src := `package fixture
import "fmt"
type Handler struct{ Fn func() }
func MountHandler() {
	_ = Handler{Fn: func() { fmt.Println("inside struct literal funclit") }}
}
`
	pkg, file := buildFakePkg(t, src)
	var target *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if target != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Println" {
			target = call
			return false
		}
		return true
	})
	require.NotNil(t, target)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, target)
	require.True(t, ok, "FuncLit inside struct literal must resolve to outer FuncDecl")
	assert.Equal(t, "MountHandler", fn.Name(),
		"identity must be outer FuncDecl, not the anonymous FuncLit nested in the struct literal")
}

func TestResolveEnclosingFunc_NestedFuncLits_ReturnsOutermost(t *testing.T) {
	// Two levels of FuncLit nesting → identity is the outermost FuncDecl.
	src := `package fixture
import "fmt"
func Outermost() {
	a := func() {
		b := func() { fmt.Println("deep") }
		b()
	}
	a()
}
`
	pkg, file := buildFakePkg(t, src)
	var target *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if target != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Println" {
			target = call
			return false
		}
		return true
	})
	require.NotNil(t, target)

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, target)
	require.True(t, ok)
	assert.Equal(t, "Outermost", fn.Name(),
		"identity must collapse to the OUTERMOST FuncDecl, not intermediate FuncLits")
}

func TestResolveEnclosingFunc_NilTypesInfo(t *testing.T) {
	src := `package fixture
import "fmt"
func F() { fmt.Println("x") }
`
	_, file := buildFakePkg(t, src)
	call := findFirstCall(t, file)

	fn, ok := ResolveEnclosingFunc(nil, file, call)
	assert.False(t, ok, "nil typesInfo must not panic and must return false")
	assert.Nil(t, fn)
}

func TestResolveEnclosingFunc_NilFile(t *testing.T) {
	src := `package fixture
import "fmt"
func F() { fmt.Println("x") }
`
	pkg, _ := buildFakePkg(t, src)
	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, nil, nil)
	assert.False(t, ok)
	assert.Nil(t, fn)
}

func TestResolveEnclosingFunc_NodeOutsideFile(t *testing.T) {
	// node.Pos() falling between two FuncDecls (e.g. in a CommentGroup or
	// import block) → (nil, false). We synthesize this by giving a node
	// whose Pos() points to the file's import declaration.
	src := `package fixture
import "fmt"
func F() { fmt.Println("x") }
`
	pkg, file := buildFakePkg(t, src)
	require.NotEmpty(t, file.Imports)
	importNode := file.Imports[0]

	fn, ok := ResolveEnclosingFunc(pkg.TypesInfo, file, importNode)
	assert.False(t, ok, "node in import block has no enclosing FuncDecl")
	assert.Nil(t, fn)
}
