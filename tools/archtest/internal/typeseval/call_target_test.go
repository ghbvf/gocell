package typeseval

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findFirstIdent returns the first *ast.Ident with the given name encountered
// in a left-to-right pre-order traversal. Used to pluck specific identifiers
// from a fixture file when their position is known by name.
func findFirstIdent(t *testing.T, file *ast.File, name string) *ast.Ident {
	t.Helper()
	var found *ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = id
			return false
		}
		return true
	})
	require.NotNilf(t, found, "ident %q not found in fixture", name)
	return found
}

// findFirstSelector returns the first *ast.SelectorExpr whose Sel.Name matches.
func findFirstSelector(t *testing.T, file *ast.File, selName string) *ast.SelectorExpr {
	t.Helper()
	var found *ast.SelectorExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == selName {
			found = sel
			return false
		}
		return true
	})
	require.NotNilf(t, found, "selector .%s not found in fixture", selName)
	return found
}

// findCallSiteIdent returns the *ast.Ident at CallExpr.Fun position for the
// given name. Distinct from findFirstIdent because the FuncDecl name position
// is in info.Defs (not Uses); we need the use-site ident.
func findCallSiteIdent(t *testing.T, file *ast.File, name string) *ast.Ident {
	t.Helper()
	var found *ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				found = id
				return false
			}
		}
		return true
	})
	require.NotNilf(t, found, "call-site ident %q not found in fixture", name)
	return found
}

func TestResolvePackageRef_QualifiedSelector(t *testing.T) {
	src := `package fixture
import "time"
func _() { time.Sleep(0) }
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Sleep")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, sel)
	require.True(t, ok)
	assert.Equal(t, "time", path)
	assert.Equal(t, "Sleep", name)
}

func TestResolvePackageRef_DotImportBareIdent(t *testing.T) {
	src := `package fixture
import . "time"
func _() { Sleep(0) }
`
	pkg, file := buildFakePkg(t, src)
	id := findFirstIdent(t, file, "Sleep")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, id)
	require.True(t, ok, "dot-imported bare Ident must resolve to (pkgPath, name)")
	assert.Equal(t, "time", path)
	assert.Equal(t, "Sleep", name)
}

func TestResolvePackageRef_MethodCallReceiver(t *testing.T) {
	src := `package fixture
import "time"
func _() {
	var t *time.Timer
	_ = t.Stop()
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Stop")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, sel)
	assert.False(t, ok, "method-position selector must not resolve as package ref")
	assert.Empty(t, path)
	assert.Empty(t, name)
}

func TestResolvePackageRef_Builtin(t *testing.T) {
	src := `package fixture
func _() { _ = len("x") }
`
	pkg, file := buildFakePkg(t, src)
	id := findCallSiteIdent(t, file, "len")

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, id)
	assert.False(t, ok, "universe builtin must not resolve as package ref")
}

func TestResolvePackageRef_TypeConversion(t *testing.T) {
	src := `package fixture
func _() { _ = int(1) }
`
	pkg, file := buildFakePkg(t, src)
	id := findCallSiteIdent(t, file, "int")

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, id)
	assert.False(t, ok, "type conversion (TypeName ident) must not resolve as package ref")
}

func TestResolvePackageRef_LocalFuncReturnsFixturePkg(t *testing.T) {
	src := `package fixture
func local() {}
func _() { local() }
`
	pkg, file := buildFakePkg(t, src)
	id := findCallSiteIdent(t, file, "local")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, id)
	require.True(t, ok, "local function call must resolve; caller filters by pkgPath")
	assert.Equal(t, "fixture", path)
	assert.Equal(t, "local", name)
}

func TestResolvePackageRef_FuncValueVar(t *testing.T) {
	src := `package fixture
import "time"
func _() {
	f := time.Sleep
	f(0)
}
`
	pkg, file := buildFakePkg(t, src)
	id := findCallSiteIdent(t, file, "f")

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, id)
	assert.False(t, ok, "func value variable must not resolve as *types.Func")
}

func TestResolvePackageRef_NestedSelector(t *testing.T) {
	src := `package fixture
type inner struct{}
func (inner) Method() {}
type outer struct{ Field inner }
func _() {
	var o outer
	o.Field.Method()
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Method")

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, sel)
	assert.False(t, ok, "nested selector (sel.X is not *ast.Ident) must not match")
}

func TestResolvePackageRef_NilGuards(t *testing.T) {
	src := `package fixture
import "time"
func _() { time.Sleep(0) }
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Sleep")

	t.Run("nil typesInfo", func(t *testing.T) {
		_, _, ok := ResolvePackageRef(nil, sel)
		assert.False(t, ok)
	})
	t.Run("nil expr", func(t *testing.T) {
		_, _, ok := ResolvePackageRef(pkg.TypesInfo, nil)
		assert.False(t, ok)
	})
}

func TestResolvePackageRef_ParenExprUnhandled(t *testing.T) {
	// Helper contract boundary: passing a *ast.ParenExpr directly returns
	// (nil, false). In real archtest matchers this is never a problem because
	// scanner.EachInSubtree recurses into ParenExpr / IndexExpr wrappers and
	// visits the inner Ident / SelectorExpr nodes directly — the helper is
	// only ever called on those inner nodes. This test pins the helper's
	// "no implicit unwrap" boundary so a future refactor cannot silently
	// start unwrapping (which would risk double-counting in callers that
	// rely on the current behavior).
	src := `package fixture
import "time"
func _() {
	x := time.Sleep
	_ = x
}
`
	pkg, file := buildFakePkg(t, src)
	parens := &ast.ParenExpr{X: findFirstSelector(t, file, "Sleep")}

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, parens)
	assert.False(t, ok, "ParenExpr boundary: helper does not unwrap")
}

func TestResolvePackageRef_DotImportNonFuncIdent(t *testing.T) {
	// Helper-level coverage for "dot-imported non-Func identifier returns
	// false" — decouples this contract from the fixture-layer
	// `dot_import_type_reference_only` test in scanner_framework_usage_test.go.
	src := `package fixture
import . "io/fs"
var _ FS
`
	pkg, file := buildFakePkg(t, src)
	id := findFirstIdent(t, file, "FS")

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, id)
	assert.False(t, ok, "dot-imported TypeName ident must not resolve as package ref")
}

// TestResolvePackageRef_PartialTypeInfoQualified models the fixture scenario
// where importer.Default() cannot load a non-stdlib package: info.Uses[sel.X]
// still resolves to *types.PkgName (the import alias is known syntactically),
// but info.Uses[sel.Sel] is nil because the symbol cannot be looked up.
// The helper must succeed on the syntactic Sel.Name without needing the
// *types.Func resolution.
//
// We cannot easily fabricate that exact info-shape inside buildFakePkg (which
// uses importer.Default()), so this test instead exercises the canonical
// stdlib case and asserts the syntactic-Sel-name contract via documentation.
// The end-to-end behavior is locked by TestScannerFrameworkUsage01_Fixture/
// direct_call_inspector_new where the import "golang.org/x/tools/..." is
// loadable in `go test` but the resolver path is the same.
func TestResolvePackageRef_PartialTypeInfoQualified(t *testing.T) {
	// fmt is stdlib so this passes through the full-info code path, but the
	// contract is: only info.Uses[sel.X] is consulted; Sel.Name is syntactic.
	src := `package fixture
import "fmt"
func _() { fmt.Println("x") }
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Println")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, sel)
	require.True(t, ok)
	assert.Equal(t, "fmt", path)
	assert.Equal(t, "Println", name, "Sel.Name returned syntactically")
}
