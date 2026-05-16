package typesutil

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"
)

// checkPkg parses + type-checks src in-memory and returns the typed
// package so tests can pull named/interface types out of package scope.
// (buildFakePkg in receiver_type_test.go returns only *types.Info, which
// is insufficient for scope lookups — hence a distinct helper.)
func checkPkg(t *testing.T, src string) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture source")
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("fixture", fset, []*ast.File{file}, nil)
	require.NoError(t, err, "type-check fixture source")
	return pkg
}

func lookupType(t *testing.T, pkg *types.Package, name string) types.Type {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	require.NotNil(t, obj, "object %q not found in fixture scope", name)
	return obj.Type()
}

func lookupIface(t *testing.T, pkg *types.Package, name string) *types.Interface {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	require.NotNil(t, obj, "interface %q not found in fixture scope", name)
	it, ok := obj.Type().Underlying().(*types.Interface)
	require.True(t, ok, "%q underlying is not *types.Interface", name)
	return it
}

// Each case states t (value or pointer form), the iface, and the expected
// result under the value-or-pointer helper vs the strict value-only one.
func TestImplementsInterface(t *testing.T) {
	const valueRecv = `package fixture
type I interface { M() }
type T struct{}
func (T) M() {}
`
	const ptrRecv = `package fixture
type I interface { M() }
type T struct{}
func (*T) M() {}
`
	const noMethod = `package fixture
type I interface { M() }
type T struct{}
`
	const promoted = `package fixture
type I interface { M() }
type Base struct{}
func (Base) M() {}
type Outer struct { Base }
`
	const generic = `package fixture
type I interface { M() }
type Box[X any] struct{ v X }
func (Box[X]) M() {}
var BoxIntVar Box[int]
`

	tests := []struct {
		name      string
		src       string
		typeName  string // looked up via package scope
		asPointer bool   // wrap looked-up type in *T before the check
		wantOrPtr bool   // ImplementsInterface (value-or-pointer)
		wantExact bool   // ImplementsInterfaceExact (value-only)
	}{
		{"value-recv impl, pass value", valueRecv, "T", false, true, true},
		{"ptr-recv only, pass value: ptr fallback hits but exact misses", ptrRecv, "T", false, true, false},
		{"ptr-recv only, pass *T", ptrRecv, "T", true, true, true},
		{"value-recv impl, pass *T: already-pointer true path, no double-wrap", valueRecv, "T", true, true, true},
		{"already *T non-impl: must not fall back to **T", noMethod, "T", true, false, false},
		{"non-impl value", noMethod, "T", false, false, false},
		{"promoted embedded method", promoted, "Outer", false, true, true},
		{"generic instantiated Box[int]", generic, "BoxIntVar", false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pkg := checkPkg(t, tc.src)
			typ := lookupType(t, pkg, tc.typeName)
			if tc.asPointer {
				typ = types.NewPointer(typ)
			}
			iface := lookupIface(t, pkg, "I")
			require.Equal(t, tc.wantOrPtr, ImplementsInterface(typ, iface),
				"ImplementsInterface")
			require.Equal(t, tc.wantExact, ImplementsInterfaceExact(typ, iface),
				"ImplementsInterfaceExact")
		})
	}
}

func TestImplementsInterface_NilGuards(t *testing.T) {
	pkg := checkPkg(t, `package fixture
type I interface { M() }
type T struct{}
func (T) M() {}
`)
	typ := lookupType(t, pkg, "T")
	iface := lookupIface(t, pkg, "I")

	require.False(t, ImplementsInterface(nil, iface), "nil type → false")
	require.False(t, ImplementsInterface(typ, nil), "nil iface → false")
	require.False(t, ImplementsInterfaceExact(nil, iface), "exact nil type → false")
	require.False(t, ImplementsInterfaceExact(typ, nil), "exact nil iface → false")
}
