package typeseval

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"runtime"
	"sync"
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
	root := findArchTestModuleRoot(t)
	_, errs, err := LoadPackages(root, "./tools/archtest/testdata/nonexistent/...")
	require.NoError(t, err, "loader itself should not error on missing pattern")
	assert.NotEmpty(t, errs, "missing pattern surfaces packages.Error entries via Visit")
	// Each error message must be prefixed with the modRoot for easy diagnosis.
	for _, e := range errs {
		assert.Contains(t, e.Msg, root, "error message should contain modRoot for easy diagnosis")
	}
}

func TestSharedResolver_Singleton(t *testing.T) {
	root := findArchTestModuleRoot(t)
	t.Cleanup(func() {
		sharedMu.Lock()
		key := root + "\x00" + "./tools/archtest/internal/typeseval/..."
		delete(sharedCache, key)
		sharedMu.Unlock()
	})

	r1, err := SharedResolver(root, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	r2, err := SharedResolver(root, "./tools/archtest/internal/typeseval/...")
	require.NoError(t, err)
	assert.Same(t, r1, r2, "SharedResolver should return cached singleton for same key")
}

// TestSharedResolver_ConcurrentInit verifies that concurrent callers with a
// cache-miss key all receive the same *Resolver and the race detector stays
// clean. Each goroutine uses the same unique pattern so only one Load occurs.
func TestSharedResolver_ConcurrentInit(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pattern := "./tools/archtest/internal/typeseval/..."
	key := root + "\x00" + pattern

	// Pre-clean so this test always exercises the miss path.
	sharedMu.Lock()
	delete(sharedCache, key)
	sharedMu.Unlock()

	t.Cleanup(func() {
		sharedMu.Lock()
		delete(sharedCache, key)
		sharedMu.Unlock()
	})

	const N = 8
	results := make([]*Resolver, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		i := i
		go func() {
			defer wg.Done()
			results[i], errs[i] = SharedResolver(root, pattern)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "goroutine %d got error", i)
	}
	for i := 1; i < N; i++ {
		assert.Same(t, results[0], results[i], "goroutine %d got different *Resolver", i)
	}
}

// TestSharedResolver_DifferentKeysIsolated verifies that two calls to
// SharedResolver with distinct pattern sets return different *Resolver
// instances backed by independently loaded package sets.
func TestSharedResolver_DifferentKeysIsolated(t *testing.T) {
	root := findArchTestModuleRoot(t)
	patternA := "./tools/archtest/internal/typeseval/..."
	patternB := "./pkg/..."
	keyA := root + "\x00" + patternA
	keyB := root + "\x00" + patternB

	// Pre-clean to avoid cross-test pollution from the Singleton test.
	sharedMu.Lock()
	delete(sharedCache, keyA)
	delete(sharedCache, keyB)
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		delete(sharedCache, keyA)
		delete(sharedCache, keyB)
		sharedMu.Unlock()
	})

	rA, err := SharedResolver(root, patternA)
	require.NoError(t, err)
	rB, err := SharedResolver(root, patternB)
	require.NoError(t, err)

	assert.NotSame(t, rA, rB, "different patterns must return distinct *Resolver instances")

	namesA := packageNames(rA)
	namesB := packageNames(rB)
	assert.Contains(t, namesA, "typeseval", "rA should contain typeseval package")
	assert.NotEqual(t, namesA, namesB, "resolvers with different patterns should have different package sets")
}

// TestSharedResolver_FailureNotCached verifies that a failed SharedResolver
// call does not poison the cache: a subsequent call with the same key must
// also attempt to load (and fail again), not return a nil *Resolver silently.
func TestSharedResolver_FailureNotCached(t *testing.T) {
	root := findArchTestModuleRoot(t)
	// A pattern that will never match any package in the module.
	badPattern := "./tools/archtest/testdata/nonexistent/..."
	key := root + "\x00" + badPattern

	// Pre-clean so this test always exercises the miss path.
	sharedMu.Lock()
	delete(sharedCache, key)
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		delete(sharedCache, key)
		sharedMu.Unlock()
	})

	// First call: must fail.
	r1, err1 := SharedResolver(root, badPattern)
	assert.Nil(t, r1, "first call with bad pattern should return nil resolver")
	assert.Error(t, err1, "first call with bad pattern should return an error")

	// Cache must not have been populated.
	sharedMu.Lock()
	_, cached := sharedCache[key]
	sharedMu.Unlock()
	assert.False(t, cached, "failed SharedResolver must not write to sharedCache")

	// Second call with same key: must also fail (not return a silent nil).
	r2, err2 := SharedResolver(root, badPattern)
	assert.Nil(t, r2, "second call with bad pattern should still return nil resolver")
	assert.Error(t, err2, "failure result must not be cached — second call must also return an error")
}

// packageNames returns the set of package names from the resolver's loaded packages.
func packageNames(r *Resolver) map[string]struct{} {
	names := make(map[string]struct{}, len(r.Packages()))
	for _, p := range r.Packages() {
		names[p.Name] = struct{}{}
	}
	return names
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
