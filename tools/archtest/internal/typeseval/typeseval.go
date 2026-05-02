// Package typeseval provides go/types-backed helpers for archtest scanners.
//
// Scope: archtest internal helper. Not exported beyond tools/archtest because
// kernel/governance enforces stdlib-only and runtime/cells/adapters have no
// reason to evaluate AST constants.
//
// The helpers cover two patterns:
//
//  1. EvaluateConstString — collapse BasicLit / Ident / SelectorExpr / BinaryExpr
//     to their compile-time string constant value via go/types' built-in
//     constant folding.
//  2. LoadPackages / SharedResolver — load a module subtree with full type info
//     once, then resolve any *ast.Expr to its constant via the owning
//     packages.Package. Both accept a `tests` flag (true loads test variant
//     packages, including *_test.go files) and a `tags` slice (joined into
//     -tags=a,b,c BuildFlags).
//
// ref: golang.org/x/tools/go/packages — NeedTypesInfo + constant folding
// ref: go/types TypesInfo.Types — maps ast.Expr to TypeAndValue (incl. const)
package typeseval

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
	"golang.org/x/tools/go/packages"
)

// EvaluateConstString returns the compile-time string constant value of expr,
// or ("", false) when expr is not a constant string.
func EvaluateConstString(typesInfo *types.Info, expr ast.Expr) (string, bool) {
	if typesInfo == nil {
		return "", false
	}
	tv, ok := typesInfo.Types[expr]
	if !ok || tv.Value == nil {
		return "", false
	}
	if tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// Resolver wraps a loaded set of packages for repeated constant evaluation.
type Resolver struct {
	pkgs []*packages.Package
}

// Packages returns the loaded packages slice.
func (r *Resolver) Packages() []*packages.Package {
	return r.pkgs
}

// NewResolverFromPackages wraps an externally loaded packages slice in a
// Resolver. It is the injection point for callers (notably archtest's
// TestMain, which shares a single packages.Load with tools/depgraph) to
// avoid running packages.Load twice per archtest invocation.
//
// Caller is responsible for loading with the modes typeseval needs
// (NeedTypesInfo + NeedSyntax + NeedTypes); SharedResolver and
// LoadPackages set them automatically. depgraph.Load uses the same
// superset.
func NewResolverFromPackages(pkgs []*packages.Package) *Resolver {
	return &Resolver{pkgs: pkgs}
}

// LoadPackages loads patterns from modRoot with full type info.
//
// Parameters:
//   - tests: when true, load the test variant of each package (includes
//     *_test.go and adds a synthetic xtest package for `package x_test`).
//   - tags: joined as `-tags=a,b,c` in BuildFlags; pass nil/empty to omit.
//
// Returns the flat slice of packages.Errors collected from every package as
// the second value so callers can fail fast on type-check errors without
// re-walking.
func LoadPackages(modRoot string, tests bool, tags []string, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports |
			packages.NeedDeps,
		Dir:   modRoot,
		Tests: tests,
	}
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, nil, fmt.Errorf("packages.Load: %w", err)
	}
	var errs []packages.Error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i := range p.Errors {
			p.Errors[i].Msg = modRoot + ": " + p.Errors[i].Msg
		}
		errs = append(errs, p.Errors...)
	})
	return pkgs, errs, nil
}

var (
	sharedMu    sync.Mutex
	sharedCache = map[string]*Resolver{}
	sharedGroup singleflight.Group
)

// SharedResolver returns a process-wide cached Resolver keyed on
// (modRoot, tests, tags, patterns). Successive callers with the same key
// reuse the loaded packages. Errors are not cached — a transient failure
// does not poison subsequent calls.
//
// Cache keys are formed by joining modRoot, the tests flag, the tag list,
// and each pattern with NUL bytes. NUL is illegal in POSIX paths and Go
// import patterns, so collisions are impossible even when patterns
// themselves contain "|" or ",".
//
// Concurrency: the cache is read and written under sharedMu, but the
// expensive LoadPackages call runs without the lock. singleflight
// deduplicates concurrent loads of the same key so only one packages.Load
// is in flight per key, while loads for different keys run in parallel.
func SharedResolver(modRoot string, tests bool, tags []string, patterns ...string) (*Resolver, error) {
	testsFlag := "0"
	if tests {
		testsFlag = "1"
	}
	key := modRoot + "\x00" + testsFlag + "\x00" + strings.Join(tags, "\x00") + "\x00" + strings.Join(patterns, "\x00")

	sharedMu.Lock()
	if r, ok := sharedCache[key]; ok {
		sharedMu.Unlock()
		return r, nil
	}
	sharedMu.Unlock()

	v, err, _ := sharedGroup.Do(key, func() (any, error) {
		// Re-check inside the singleflight group: another caller may have
		// populated the cache between our miss and entering Do.
		sharedMu.Lock()
		if r, ok := sharedCache[key]; ok {
			sharedMu.Unlock()
			return r, nil
		}
		sharedMu.Unlock()

		pkgs, errs, err := LoadPackages(modRoot, tests, tags, patterns...)
		if err != nil {
			return nil, err
		}
		if len(errs) > 0 {
			return nil, fmt.Errorf("packages.Load: %d error(s): first=%v", len(errs), errs[0])
		}
		r := &Resolver{pkgs: pkgs}

		sharedMu.Lock()
		sharedCache[key] = r
		sharedMu.Unlock()
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Resolver), nil
}
