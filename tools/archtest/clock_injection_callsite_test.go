// CLOCK-INJECTION-TEST-CALLSITE-01 — test-callsite gate for Clock injection.
//
// Invariant: In every *_test.go file, any call to a constructor that accepts
// variadic Option parameters and whose package exports a WithClock(Clock) Option
// function must pass WithClock(...) as one of the option arguments.
//
// Motivation: CI-01/02/03 panics share a common root cause — build-tag tests
// call service constructors that enforce Clock via clock.MustHaveClock, but
// the test call-site omits the WithClock option. Static analysis at package
// load time catches this class of error before runtime.
//
// Detection strategy (option-pattern only, v1):
//  1. Load all packages with tests=true and the integration+e2e build tags.
//  2. For each non-test file, collect packages that export a function named
//     "WithClock" (the canonical Clock option injector). Record the package
//     path and the set of constructor names — functions in the same package
//     whose last parameter is variadic and whose name starts with "New".
//  3. Scan each test file for CallExpr whose callee resolves (via go/types) to
//     one of the collected constructors.
//  4. For each such call, walk the argument list looking for a nested CallExpr
//     whose callee resolves to the WithClock function of the same package.
//  5. If no WithClock call is found among the arguments — report a violation.
//
// Scope: option-pattern ("WithClock(clk) as option arg"). Positional Clock
// parameters are out of scope for v1.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// clockRequiredCtor holds a collected constructor whose package has a WithClock
// option function.
type clockRequiredCtor struct {
	ctorFullName      string // key: ctor.FullName()
	withClockFullName string // key: withClock.FullName()
}

// collectClockRequiredCtors scans all loaded packages and collects constructors
// (func name starting with "New", last param variadic) whose package also
// exports a "WithClock" function.
//
// Returns a map keyed by constructor FullName() for pointer-stable lookup.
// Using FullName() strings instead of *types.Func pointers avoids false
// mismatches between the test-variant and non-test-variant of the same package
// (packages.Load with Tests=true loads the package twice: the normal variant
// and the "test variant" each get a distinct *types.Package, so two different
// *types.Func pointers refer to the same logical function).
func collectClockRequiredCtors(pkgs []*packages.Package) map[string]clockRequiredCtor {
	// First pass: collect packages that export WithClock.
	// Key: package path (may differ between normal/test variant, but path is same).
	type pkgInfo struct {
		withClockFullName string
	}
	pkgWithClock := map[string]pkgInfo{} // keyed by pkg.Path()
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil {
			return
		}
		pkgPath := p.Types.Path()
		if visited[pkgPath] {
			return
		}
		visited[pkgPath] = true

		scope := p.Types.Scope()
		obj := scope.Lookup("WithClock")
		if obj == nil {
			return
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return
		}
		pkgWithClock[pkgPath] = pkgInfo{withClockFullName: fn.FullName()}
	})

	// Second pass: collect New* constructors from packages that have WithClock.
	result := map[string]clockRequiredCtor{}
	visited = map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil {
			return
		}
		pkgPath := p.Types.Path()
		if visited[pkgPath] {
			return
		}
		visited[pkgPath] = true

		info, hasWithClock := pkgWithClock[pkgPath]
		if !hasWithClock {
			return
		}

		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			if !strings.HasPrefix(name, "New") {
				continue
			}
			obj := scope.Lookup(name)
			fn, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok || !sig.Variadic() {
				continue
			}
			key := fn.FullName()
			result[key] = clockRequiredCtor{
				ctorFullName:      key,
				withClockFullName: info.withClockFullName,
			}
		}
	})

	return result
}

// callsWithClock reports whether any of the call arguments in args contains a
// direct CallExpr whose callee's FullName matches withClockFullName.
func callsWithClock(args []ast.Expr, info *types.Info, withClockFullName string) bool {
	for _, arg := range args {
		call, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		fn := resolvedFunc(call.Fun, info)
		if fn != nil && fn.FullName() == withClockFullName {
			return true
		}
	}
	return false
}

// resolvedFunc returns the *types.Func for a call expression's function
// expression, or nil if it cannot be determined.
func resolvedFunc(fun ast.Expr, info *types.Info) *types.Func {
	if info == nil {
		return nil
	}
	var ident *ast.Ident
	switch e := fun.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return nil
	}
	obj, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return nil
	}
	return obj
}

// scanClockCallsiteAST walks file looking for calls to any constructor in
// ctors from test files, and reports violations where WithClock is missing.
func scanClockCallsiteAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	ctors map[string]clockRequiredCtor,
) []string {
	var out []string
	seen := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee := resolvedFunc(call.Fun, info)
		if callee == nil {
			return true
		}
		ctor, isCtor := ctors[callee.FullName()]
		if !isCtor {
			return true
		}
		// Only flag calls that have at least one option argument (variadic slice
		// non-empty). A bare NewXxx() with zero options has no WithXxx at all,
		// which may be intentional (e.g. constructor with zero required options).
		// We only flag the case where options ARE passed but WithClock is absent.
		if len(call.Args) == 0 {
			return true
		}
		if callsWithClock(call.Args, info, ctor.withClockFullName) {
			return true
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s", rel, line, callee.Name())
		if !seen[key] {
			seen[key] = true
			out = append(out, fmt.Sprintf(
				"%s:%d: %s called without WithClock — "+
					"must pass WithClock(clk) to satisfy the clock injection requirement. "+
					"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md",
				rel, line, callee.FullName()))
		}
		return true
	})

	sort.Strings(out)
	return out
}

// TestClockInjectionCallsite enforces CLOCK-INJECTION-TEST-CALLSITE-01:
// test files must pass WithClock when calling constructors whose package
// exports WithClock.
func TestClockInjectionCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	// Load with tests=true so *_test.go files are included in Syntax.
	pkgs, errs, err := typeseval.LoadPackages(root, true, testTimeLiteralBuildTags, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	ctors := collectClockRequiredCtors(pkgs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			// Only scan test files.
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}

			violations = append(violations,
				scanClockCallsiteAST(p.Fset, file, rel, p.TypesInfo, ctors)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CLOCK-INJECTION-TEST-CALLSITE-01: test code must pass WithClock(clk) "+
			"when calling constructors that enforce Clock injection. "+
			"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md")
}

// runClockCallsiteFixtureScan loads a fixture directory and returns violations.
func runClockCallsiteFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(fixtureDir, true, nil, "./...")
	require.NoError(t, err, "packages.Load failed for fixture %s", fixtureDir)
	require.Empty(t, errs, "package load errors for %s: %v", fixtureDir, errs)

	ctors := collectClockRequiredCtors(pkgs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(fixtureDir, abs)
			if !ok {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			violations = append(violations,
				scanClockCallsiteAST(p.Fset, file, rel, p.TypesInfo, ctors)...)
		}
	})

	sort.Strings(violations)
	return violations
}

// TestClockInjectionCallsiteFixtures validates fixture-based regression cases.
func TestClockInjectionCallsiteFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/clock_injection_callsite_fixtures"

	cases := []struct {
		pkg           string
		wantViolCount int
	}{
		{"compliant", 0},
		{"violates", 1},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runClockCallsiteFixtureScan(t, base+"/"+tc.pkg)
			assert.Equal(t, tc.wantViolCount, len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, tc.wantViolCount, len(got), got)
		})
	}
}
