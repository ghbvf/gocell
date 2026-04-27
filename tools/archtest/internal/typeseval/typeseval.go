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
//  2. LoadPackages / Resolver / SharedResolver — load a module subtree with
//     full type info once, then resolve any *ast.Expr to its constant via the
//     owning packages.Package.
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

// LoadPackages loads patterns from modRoot with full type info. Returns the
// flat slice of packages.Errors collected from every package as the second
// value so callers can fail fast on type-check errors without re-walking.
func LoadPackages(modRoot string, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports |
			packages.NeedDeps,
		Dir: modRoot,
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

// NewResolver loads patterns from modRoot and returns a Resolver. Type-check
// errors in any loaded package are turned into a single error so callers do
// not act on partial type information.
func NewResolver(modRoot string, patterns ...string) (*Resolver, error) {
	pkgs, errs, err := LoadPackages(modRoot, patterns...)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("packages.Load: %d error(s): first=%v", len(errs), errs[0])
	}
	return &Resolver{pkgs: pkgs}, nil
}

var (
	sharedMu    sync.Mutex
	sharedCache = map[string]*Resolver{}
)

// SharedResolver returns a process-wide cached Resolver keyed on (modRoot,
// patterns). Successive callers with the same key reuse the loaded packages.
// Errors are not cached — a transient failure does not poison subsequent calls.
//
// Cache keys are formed by joining modRoot and each pattern with NUL bytes.
// NUL is illegal in POSIX paths and Go import patterns, so collisions are
// impossible even when patterns themselves contain "|" or ",".
func SharedResolver(modRoot string, patterns ...string) (*Resolver, error) {
	key := modRoot + "\x00" + strings.Join(patterns, "\x00")
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if r, ok := sharedCache[key]; ok {
		return r, nil
	}
	r, err := NewResolver(modRoot, patterns...)
	if err != nil {
		return nil, err
	}
	sharedCache[key] = r
	return r, nil
}
