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

// findNthIdent returns the n-th *ast.Ident (1-based) with the given name
// encountered in a left-to-right pre-order traversal. Use n>1 to skip
// definition-site occurrences (which are in info.Defs, not info.Uses).
func findNthIdent(t *testing.T, file *ast.File, name string, n int) *ast.Ident {
	t.Helper()
	var count int
	var found *ast.Ident
	ast.Inspect(file, func(node ast.Node) bool {
		if found != nil {
			return false
		}
		if id, ok := node.(*ast.Ident); ok && id.Name == name {
			count++
			if count == n {
				found = id
				return false
			}
		}
		return true
	})
	require.NotNilf(t, found, "ident %q occurrence %d not found in fixture", name, n)
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
	// ("", "", false). In real archtest matchers this is never a problem because
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

func TestResolvePackageRef_DotImportTypeNameIdent(t *testing.T) {
	// After the *types.TypeName extension: a dot-imported package-level type
	// (struct, interface, etc.) must resolve to (pkgPath, name, true).
	// This is the root-cause fix for the ImportBan blind spot: bare Ident
	// whose Uses[id] is *types.TypeName must be resolved just like *types.Func.
	src := `package fixture
import . "io/fs"
var _ FS
`
	pkg, file := buildFakePkg(t, src)
	id := findFirstIdent(t, file, "FS")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, id)
	require.True(t, ok, "dot-imported TypeName ident must now resolve as package ref")
	assert.Equal(t, "io/fs", path)
	assert.Equal(t, "FS", name)
}

func TestResolvePackageRef_DotImportTypeAliasIdent(t *testing.T) {
	// Specifically exercises the type ALIAS shape: the ImportBan fixture uses
	// type ImportBan struct{} in scanner, accessed via dot-import as bare Ident.
	// Uses[id] is *types.TypeName where IsAlias()=false (it's the canonical
	// type in its declaring package). tn.Pkg().Path() returns the scanner
	// package path; tn.Name() returns "ImportBan". This pinned tuple must
	// match what the banned map keys on (the same path+name that the qualified
	// SelectorExpr scanner.ImportBan returns).
	//
	// We simulate with net/http.Request (a stdlib struct, same shape as
	// scanner.ImportBan): dot-imported, accessed as bare Ident.
	src := `package fixture
import . "net/http"
var _ *Request
`
	pkg, file := buildFakePkg(t, src)
	id := findFirstIdent(t, file, "Request")

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, id)
	require.True(t, ok, "dot-imported struct TypeName ident must resolve as package ref")
	assert.Equal(t, "net/http", path)
	assert.Equal(t, "Request", name)
}

func TestResolvePackageRef_LocalTypeNameNotResolved(t *testing.T) {
	// A locally-defined type used via bare Ident (TypeName in the current
	// package) resolves with the current package path — callers must filter by
	// pkgPath to distinguish cross-package vs local references. This is the
	// same contract as TestResolvePackageRef_LocalFuncReturnsFixturePkg for
	// *types.Func.
	//
	// The usage ident (second occurrence in `var _ MyType`) is in info.Uses;
	// the first occurrence (in `type MyType struct{}`) is in info.Defs only.
	// findNthIdent is used to skip the definition site.
	src := `package fixture
type MyType struct{}
var _ MyType
`
	pkg, file := buildFakePkg(t, src)
	id := findNthIdent(t, file, "MyType", 2)

	path, name, ok := ResolvePackageRef(pkg.TypesInfo, id)
	require.True(t, ok, "local TypeName use-site resolves; callers filter by pkgPath")
	assert.Equal(t, "fixture", path)
	assert.Equal(t, "MyType", name)
}

func TestResolvePackageRef_DotImportNonFuncNonTypeIdent(t *testing.T) {
	// A dot-imported VAR (not Func, not TypeName) still returns ok=false.
	// Only *types.Func and *types.TypeName are handled at the bare-Ident
	// position; *types.Var (including package-level vars) is not.
	src := `package fixture
import . "os"
var _ = Stderr
`
	pkg, file := buildFakePkg(t, src)
	id := findFirstIdent(t, file, "Stderr")

	_, _, ok := ResolvePackageRef(pkg.TypesInfo, id)
	assert.False(t, ok, "dot-imported *types.Var ident must not resolve as package ref")
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
