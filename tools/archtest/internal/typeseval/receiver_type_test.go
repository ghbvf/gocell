package typeseval

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMethodCall_PointerReceiverStdlib(t *testing.T) {
	src := `package fixture
import "os"
func _() {
	f, _ := os.Open("/")
	_ = f.Name()
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Name")

	fn, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	require.True(t, ok, "(*os.File).Name() must resolve to *types.Func")
	assert.Equal(t, "os", fn.Pkg().Path())
	assert.Equal(t, "Name", fn.Name())
}

func TestResolveMethodCall_DirectInterfaceReceiver(t *testing.T) {
	src := `package fixture
import "io/fs"
func _(fsys fs.ReadDirFS) error {
	_, err := fsys.ReadDir(".")
	return err
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "ReadDir")

	fn, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	require.True(t, ok)
	assert.Equal(t, "io/fs", fn.Pkg().Path())
	assert.Equal(t, "ReadDir", fn.Name())
}

func TestResolveMethodCall_PromotedViaStructEmbedding(t *testing.T) {
	// PR469-review-round-2 P1 RED case: struct embedding promotes ReadDir.
	// The pre-PR fix (NamedTypeImportPath via sel.X type) returned "fixture"
	// (the wrapping struct's package); the Selections-based resolver picks the
	// actual method's owning package "io/fs".
	src := `package fixture
import "io/fs"
type Wrap struct{ fs.ReadDirFS }
func _(w *Wrap) error {
	_, err := w.ReadDir(".")
	return err
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "ReadDir")

	fn, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	require.True(t, ok, "promoted method via struct embedding must resolve")
	assert.Equal(t, "io/fs", fn.Pkg().Path(),
		"Selections.Obj() returns the embedded interface's method, not the wrapper's package")
	assert.Equal(t, "ReadDir", fn.Name())
}

func TestResolveMethodCall_NamedTypeDefinitionOfInterface(t *testing.T) {
	src := `package fixture
import "io/fs"
type MyFS fs.ReadDirFS
func _(x MyFS) error {
	_, err := x.ReadDir(".")
	return err
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "ReadDir")

	fn, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	require.True(t, ok, "method via named type definition of an interface must resolve")
	assert.Equal(t, "io/fs", fn.Pkg().Path())
	assert.Equal(t, "ReadDir", fn.Name())
}

func TestResolveMethodCall_GenericTypeParamConstraint(t *testing.T) {
	src := `package fixture
import "io/fs"
func _[F fs.ReadDirFS](x F) error {
	_, err := x.ReadDir(".")
	return err
}
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "ReadDir")

	fn, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	require.True(t, ok, "method on type parameter constrained by an interface must resolve")
	assert.Equal(t, "io/fs", fn.Pkg().Path())
	assert.Equal(t, "ReadDir", fn.Name())
}

func TestResolveMethodCall_QualifiedSelectorReturnsFalse(t *testing.T) {
	// Qualified identifier `pkg.Func` is in info.Uses, NOT info.Selections —
	// ResolvePackageRef handles that shape. ResolveMethodCall must not match it.
	src := `package fixture
import "time"
func _() { time.Sleep(0) }
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "Sleep")

	_, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	assert.False(t, ok, "qualified `pkg.Func` is not a MethodVal selection")
}

func TestResolveMethodCall_FieldSelectorReturnsFalse(t *testing.T) {
	src := `package fixture
type S struct{ X int }
func _(s S) int { return s.X }
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "X")

	_, ok := ResolveMethodCall(pkg.TypesInfo, sel)
	assert.False(t, ok, "field-position selector (FieldVal) must not resolve as method")
}

func TestResolveMethodCall_NilGuards(t *testing.T) {
	src := `package fixture
import "io/fs"
func _(fsys fs.ReadDirFS) error { _, err := fsys.ReadDir("."); return err }
`
	pkg, file := buildFakePkg(t, src)
	sel := findFirstSelector(t, file, "ReadDir")

	t.Run("nil typesInfo", func(t *testing.T) {
		_, ok := ResolveMethodCall(nil, sel)
		assert.False(t, ok)
	})
	t.Run("nil sel", func(t *testing.T) {
		_, ok := ResolveMethodCall(pkg.TypesInfo, nil)
		assert.False(t, ok)
	})
}

func TestResolveMethodCall_NonSelectorIgnored(t *testing.T) {
	// Sanity: the helper takes *ast.SelectorExpr; passing a synthesized one with
	// no entry in info.Selections must return (nil, false), not panic.
	src := `package fixture
func _() {}
`
	pkg, _ := buildFakePkg(t, src)
	synthetic := &ast.SelectorExpr{X: &ast.Ident{Name: "x"}, Sel: &ast.Ident{Name: "y"}}

	_, ok := ResolveMethodCall(pkg.TypesInfo, synthetic)
	assert.False(t, ok, "synthetic SelectorExpr absent from info.Selections returns false")
}
