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

// TestLoadMode_NoNeedDeps guards the #1499 RSS optimization: the shared
// go/packages load mode MUST NOT request packages.NeedDeps. NeedDeps keeps full
// Syntax(AST)+TypesInfo resident for every transitive dependency package — the
// dominant RSS increment of a single packages.Load (~954MB→149MB tests=F /
// ~1337MB→509MB tests=T). Without NeedDeps, go/packages satisfies dependency
// types from export data (gcexportdata), which leaves the root package's
// Types/TypesInfo/Syntax untouched and resolves cross-package symbols (info.Uses
// / types.Implements) identically. This is exactly go/packages' usesExportData
// fast path (NeedTypes && !NeedDeps) and matches go/analysis' LoadSyntax preset.
//
// The required bits below MUST stay present — a too-aggressive trim would break
// typed resolution across the archtest suite.
//
// Rating: Medium (type-aware value assertion on the typed packages.LoadMode
// const; not a string-anchor). Hard is unreachable — packages.LoadMode is a
// public int-flag type, so "no NeedDeps anywhere" cannot be made
// type-system-unexpressible (analogous to the documented won't-do ceilings
// #851/#893/#1282). No Soft grep-for-"NeedDeps" guard is added.
func TestLoadMode_NoNeedDeps(t *testing.T) {
	if loadMode&packages.NeedDeps != 0 {
		t.Fatalf("loadMode must NOT include packages.NeedDeps (#1499 RSS optimization); "+
			"go/packages satisfies dependency types from export data without it. mode=%b", loadMode)
	}

	required := []struct {
		name string
		bit  packages.LoadMode
	}{
		{"NeedName", packages.NeedName},
		{"NeedFiles", packages.NeedFiles},
		{"NeedSyntax", packages.NeedSyntax},
		{"NeedTypes", packages.NeedTypes},
		{"NeedTypesInfo", packages.NeedTypesInfo},
		{"NeedImports", packages.NeedImports},
	}
	for _, r := range required {
		if loadMode&r.bit == 0 {
			t.Fatalf("loadMode missing required bit %s — typed resolution would break", r.name)
		}
	}
}

func buildFakePkg(t *testing.T, src string) (*packages.Package, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture source")

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Uses:       make(map[*ast.Ident]types.Object),
		Defs:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
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

func TestEvaluateConstString_ViaPackage(t *testing.T) {
	src := `package fixture
import "fmt"
const Topic = "x.y.z"
func init() { fmt.Println(Topic) }
`
	pkg, file := buildFakePkg(t, src)
	args := firstCallArgs(t, file)

	v, ok := EvaluateConstString(pkg.TypesInfo, args[0])
	assert.True(t, ok)
	assert.Equal(t, "x.y.z", v)

	_, ok = EvaluateConstString(nil, args[0])
	assert.False(t, ok, "nil TypesInfo should not panic")

	_, ok = EvaluateConstString((&packages.Package{TypesInfo: nil}).TypesInfo, args[0])
	assert.False(t, ok, "nil TypesInfo should not panic")
}

func TestLoadPackages_HappyPath(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pkgs, errs, err := LoadPackages(root, false, nil, "./tools/archtest/internal/typeseval/...")
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

func TestLoadPackages_RoutesImportPathPatternToSatellite(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pkgs, errs, err := LoadPackages(root, false, nil, "github.com/ghbvf/gocell/tools/archtest/internal/typeseval")
	require.NoError(t, err)
	require.Empty(t, errs, "load errors: %v", errs)

	require.NotNil(t, packageByPath(pkgs, "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"))
}

func TestLoadPackages_MixedRootAndSatellitePatternsShareTypeIdentity(t *testing.T) {
	root := findArchTestModuleRoot(t)
	t.Setenv("GOWORK", "off")

	pkgs, errs, err := LoadPackages(root, false, nil, "./framework/kernel/metadata", "./tools/workspace")
	require.NoError(t, err)
	require.Empty(t, errs, "load errors: %v", errs)

	metadataPkg := packageByPath(pkgs, "github.com/ghbvf/gocell/framework/kernel/metadata")
	require.NotNil(t, metadataPkg, "root metadata package should be loaded")
	workspacePkg := packageByPath(pkgs, "github.com/ghbvf/gocell/tools/workspace")
	require.NotNil(t, workspacePkg, "tools workspace package should be loaded")

	importedMetadata := workspacePkg.Imports["github.com/ghbvf/gocell/framework/kernel/metadata"]
	require.NotNil(t, importedMetadata, "tools/workspace should import root kernel/metadata")
	require.Same(t, metadataPkg.Types, importedMetadata.Types, "workspace load must preserve shared type package identity")

	loadedType := metadataPkg.Types.Scope().Lookup("ManifestSpec")
	importedType := importedMetadata.Types.Scope().Lookup("ManifestSpec")
	require.NotNil(t, loadedType)
	require.NotNil(t, importedType)
	assert.True(t, types.Identical(loadedType.Type(), importedType.Type()))
}

func TestLoadPackages_PropagatesErrors(t *testing.T) {
	root := findArchTestModuleRoot(t)
	_, errs, err := LoadPackages(root, false, nil, "./tools/archtest/testdata/nonexistent/...")
	require.NoError(t, err, "loader itself should not error on missing pattern")
	assert.NotEmpty(t, errs, "missing pattern surfaces packages.Error entries via Visit")
	// Each error message must be prefixed with the modRoot for easy diagnosis.
	for _, e := range errs {
		assert.Contains(t, e.Msg, root, "error message should contain modRoot for easy diagnosis")
	}
}

func TestLoadPackages_TestsFlagIncludesTestFiles(t *testing.T) {
	root := findArchTestModuleRoot(t)

	// With tests=false, *_test.go files of typeseval are not loaded.
	pkgsNoTests, errs, err := LoadPackages(root, false, nil, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	require.Empty(t, errs)
	hasTestVariantWhenOff := false
	for _, p := range pkgsNoTests {
		// Test variant package IDs typically look like "<id> [<id>.test]" or
		// end with "_test" / ".test"; production-only load should not include them.
		if p.Name == "typeseval" {
			for _, f := range p.GoFiles {
				if filepath.Base(f) == "typeseval_test.go" {
					hasTestVariantWhenOff = true
				}
			}
		}
	}
	assert.False(t, hasTestVariantWhenOff, "tests=false must not include _test.go files in GoFiles")

	// With tests=true, the test variant of typeseval is loaded and includes the test file.
	pkgsTests, errs, err := LoadPackages(root, true, nil, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	require.Empty(t, errs)
	hasTestFile := false
	for _, p := range pkgsTests {
		for _, f := range p.GoFiles {
			if filepath.Base(f) == "typeseval_test.go" {
				hasTestFile = true
			}
		}
	}
	assert.True(t, hasTestFile, "tests=true must load typeseval_test.go via test variant")
}

// TestSharedResolver_DelegatesToCache verifies SharedResolver is backed by the
// process-wide packagesload cache (#2165): a repeated call with the same key
// reuses the loaded packages (identical underlying *packages.Package pointers),
// even though each call mints a fresh *Resolver wrapper. The cache mechanics
// (singleflight dedup, failure-not-cached, key isolation) are covered directly
// in tools/packagesload/cache_test.go; this guards the typeseval delegation.
func TestSharedResolver_DelegatesToCache(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pattern := "./tools/archtest/internal/typeseval/..."

	r1, err := SharedResolver(root, false, nil, pattern)
	require.NoError(t, err)
	r2, err := SharedResolver(root, false, nil, pattern)
	require.NoError(t, err)

	require.NotEmpty(t, r1.Packages())
	require.Same(t, r1.Packages()[0], r2.Packages()[0],
		"SharedResolver must reuse the packagesload cache; a repeat call re-loaded packages")

	// A distinct pattern loads an independent package set.
	rOther, err := SharedResolver(root, false, nil, "./framework/pkg/...")
	require.NoError(t, err)
	namesTypeseval := packageNames(r1)
	assert.Contains(t, namesTypeseval, "typeseval")
	assert.NotEqual(t, namesTypeseval, packageNames(rOther),
		"distinct patterns must load distinct package sets")
}

// TestSharedResolver_BadPatternErrors verifies resolverFrom maps a load that
// surfaces packages.Error into a fail-fast error (a partial load is a scan
// failure), returning a nil *Resolver.
func TestSharedResolver_BadPatternErrors(t *testing.T) {
	root := findArchTestModuleRoot(t)
	r, err := SharedResolver(root, false, nil, "./tools/archtest/testdata/nonexistent/...")
	assert.Nil(t, r, "a load with packages.Error must return a nil resolver")
	assert.Error(t, err, "a load with packages.Error must map to a fail-fast error")
}

// packageNames returns the set of package names from the resolver's loaded packages.
func packageNames(r *Resolver) map[string]struct{} {
	names := make(map[string]struct{}, len(r.Packages()))
	for _, p := range r.Packages() {
		names[p.Name] = struct{}{}
	}
	return names
}

func packageByPath(pkgs []*packages.Package, path string) *packages.Package {
	for _, p := range pkgs {
		if p.PkgPath == path || p.ID == path {
			return p
		}
		if p.Types != nil && p.Types.Path() == path {
			return p
		}
	}
	return nil
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
