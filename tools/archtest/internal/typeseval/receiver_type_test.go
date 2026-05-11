package typeseval

import (
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findUseSiteIdent returns the use-site (info.Types-recorded) ident for the
// named variable. Declaration-site idents only appear in info.Defs, not Types.
func findUseSiteIdent(t *testing.T, file *ast.File, name string) *ast.Ident {
	t.Helper()
	var found *ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		// Look for `_ = <ident>` blank-assign RHS as the use site.
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
			if id, ok := as.Rhs[0].(*ast.Ident); ok && id.Name == name {
				found = id
				return false
			}
		}
		return true
	})
	require.NotNilf(t, found, "use-site ident %q (blank-assign RHS) not found in fixture", name)
	return found
}

func resolveTypeOf(t *testing.T, src string, exprFinder func(*ast.File) ast.Expr) types.Type {
	t.Helper()
	pkg, file := buildFakePkg(t, src)
	expr := exprFinder(file)
	require.NotNil(t, expr, "fixture expression not found")
	tv, ok := pkg.TypesInfo.Types[expr]
	require.True(t, ok, "info.Types missing entry for fixture expr")
	return tv.Type
}

func TestNamedTypeImportPath_NilType(t *testing.T) {
	assert.Equal(t, "", NamedTypeImportPath(nil))
}

func TestNamedTypeImportPath_PointerToStdlibStruct(t *testing.T) {
	src := `package fixture
import "os"
func _() {
	var f *os.File
	_ = f
}
`
	typ := resolveTypeOf(t, src, func(f *ast.File) ast.Expr {
		return findUseSiteIdent(t, f, "f")
	})
	assert.Equal(t, "os", NamedTypeImportPath(typ))
}

func TestNamedTypeImportPath_StdlibInterface(t *testing.T) {
	src := `package fixture
import "io/fs"
func _() {
	var fsys fs.ReadDirFS
	_ = fsys
}
`
	typ := resolveTypeOf(t, src, func(f *ast.File) ast.Expr {
		return findUseSiteIdent(t, f, "fsys")
	})
	assert.Equal(t, "io/fs", NamedTypeImportPath(typ))
}

func TestNamedTypeImportPath_BasicTypeReturnsEmpty(t *testing.T) {
	src := `package fixture
func _() {
	var x int
	_ = x
}
`
	typ := resolveTypeOf(t, src, func(f *ast.File) ast.Expr {
		return findUseSiteIdent(t, f, "x")
	})
	assert.Equal(t, "", NamedTypeImportPath(typ), "universe basic type has no import path")
}

func TestNamedTypeImportPath_LocalNamedType(t *testing.T) {
	src := `package fixture
type Local struct{}
func _() {
	var x Local
	_ = x
}
`
	typ := resolveTypeOf(t, src, func(f *ast.File) ast.Expr {
		return findUseSiteIdent(t, f, "x")
	})
	assert.Equal(t, "fixture", NamedTypeImportPath(typ),
		"local named type returns current package path; caller filters")
}

func TestNamedTypeImportPath_GenericTypeParamConstraint(t *testing.T) {
	src := `package fixture
import "io/fs"
func _[F fs.ReadDirFS](fsys F) { _ = fsys }
`
	typ := resolveTypeOf(t, src, func(f *ast.File) ast.Expr {
		return findUseSiteIdent(t, f, "fsys")
	})
	assert.Equal(t, "io/fs", NamedTypeImportPath(typ),
		"type parameter constraint resolves through embedded interface")
}
