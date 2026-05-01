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

// cacheKey rebuilds the SharedResolver cache key for test cleanup. Tests in
// this file all pass nil tags, so the cache key collapses to the simpler
// shape (root + tests-flag + empty + patterns).
func cacheKey(root string, tests bool, patterns ...string) string {
	testsFlag := "0"
	if tests {
		testsFlag = "1"
	}
	out := root + "\x00" + testsFlag + "\x00" + "\x00"
	for i, p := range patterns {
		if i > 0 {
			out += "\x00"
		}
		out += p
	}
	return out
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

func TestSharedResolver_Singleton(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pattern := "./tools/archtest/internal/typeseval/..."
	t.Cleanup(func() {
		sharedMu.Lock()
		delete(sharedCache, cacheKey(root, false, pattern))
		sharedMu.Unlock()
	})

	r1, err := SharedResolver(root, false, nil, pattern)
	require.NoError(t, err)
	r2, err := SharedResolver(root, false, nil, pattern)
	require.NoError(t, err)
	assert.Same(t, r1, r2, "SharedResolver should return cached singleton for same key")
}

// TestSharedResolver_ConcurrentInit verifies that concurrent callers with a
// cache-miss key all receive the same *Resolver and the race detector stays
// clean. Each goroutine uses the same unique pattern so only one Load occurs.
//
// This is also the regression guard for singleflight deduplication: if the
// loader were called once per goroutine (no singleflight) each call would
// build its own *Resolver and the assert.Same would fail — N distinct
// pointers would race-write to sharedCache, and the goroutines that
// already returned would never see the "winning" Resolver. The current
// SharedResolver releases sharedMu during LoadPackages and lets
// singleflight collapse the in-flight calls; this test panics-out under
// `-race` if either property regresses.
func TestSharedResolver_ConcurrentInit(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pattern := "./tools/archtest/internal/typeseval/..."
	key := cacheKey(root, false, pattern)

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
		go func() {
			defer wg.Done()
			results[i], errs[i] = SharedResolver(root, false, nil, pattern)
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
	keyA := cacheKey(root, false, patternA)
	keyB := cacheKey(root, false, patternB)

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

	rA, err := SharedResolver(root, false, nil, patternA)
	require.NoError(t, err)
	rB, err := SharedResolver(root, false, nil, patternB)
	require.NoError(t, err)

	assert.NotSame(t, rA, rB, "different patterns must return distinct *Resolver instances")

	namesA := packageNames(rA)
	namesB := packageNames(rB)
	assert.Contains(t, namesA, "typeseval", "rA should contain typeseval package")
	assert.NotEqual(t, namesA, namesB, "resolvers with different patterns should have different package sets")
}

// TestSharedResolver_TestsFlagDistinctCacheKey verifies that toggling the
// tests flag yields a distinct cache entry, so a tests=false load does not
// inadvertently serve a tests=true caller (or vice versa).
func TestSharedResolver_TestsFlagDistinctCacheKey(t *testing.T) {
	root := findArchTestModuleRoot(t)
	pattern := "./tools/archtest/internal/typeseval/..."
	keyOff := cacheKey(root, false, pattern)
	keyOn := cacheKey(root, true, pattern)

	sharedMu.Lock()
	delete(sharedCache, keyOff)
	delete(sharedCache, keyOn)
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		delete(sharedCache, keyOff)
		delete(sharedCache, keyOn)
		sharedMu.Unlock()
	})

	rOff, err := SharedResolver(root, false, nil, pattern)
	require.NoError(t, err)
	rOn, err := SharedResolver(root, true, nil, pattern)
	require.NoError(t, err)
	assert.NotSame(t, rOff, rOn, "tests=false and tests=true must produce distinct cache entries")
}

// TestSharedResolver_FailureNotCached verifies that a failed SharedResolver
// call does not poison the cache: a subsequent call with the same key must
// also attempt to load (and fail again), not return a nil *Resolver silently.
func TestSharedResolver_FailureNotCached(t *testing.T) {
	root := findArchTestModuleRoot(t)
	// A pattern that will never match any package in the module.
	badPattern := "./tools/archtest/testdata/nonexistent/..."
	key := cacheKey(root, false, badPattern)

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
	r1, err1 := SharedResolver(root, false, nil, badPattern)
	assert.Nil(t, r1, "first call with bad pattern should return nil resolver")
	assert.Error(t, err1, "first call with bad pattern should return an error")

	// Cache must not have been populated.
	sharedMu.Lock()
	_, cached := sharedCache[key]
	sharedMu.Unlock()
	assert.False(t, cached, "failed SharedResolver must not write to sharedCache")

	// Second call with same key: must also fail (not return a silent nil).
	r2, err2 := SharedResolver(root, false, nil, badPattern)
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
