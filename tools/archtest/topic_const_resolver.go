package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"sync"

	"golang.org/x/tools/go/packages"
)

// topicConstResolver evaluates compile-time string constants in arbitrary
// expressions (BasicLit, Ident referring to const, SelectorExpr referring
// to const in another package). Uses go/types' built-in constant folding,
// which transparently handles intra-module cross-package const propagation
// (e.g. dto.TopicSessionCreated → "session.created.v1").
//
// The resolver is module-scoped: a single packages.Load("./cells/...")
// produces a TypesInfo per package, and the resolver dispatches ResolveString()
// based on the package owning the AST node. Loading is cached per
// resolver instance.
//
// ref: golang.org/x/tools/go/packages — NeedTypesInfo + constant folding
// ref: go/types TypesInfo.Types — maps ast.Expr to TypeAndValue (incl. const)
// ref: kernel/governance/rules_wrapper.go FMT-19 — earlier AST const scanner
//
//	(BasicLit + ValueSpec only; this resolver supersedes it for outbox
//	topic checks by going through go/types)
type topicConstResolver struct {
	pkgs      []*packages.Package
	pkgByFile map[string]*packages.Package // absolute file path → package
}

// newTopicConstResolver loads all packages matching pattern (e.g. "./cells/...")
// with full type info. The returned resolver caches the package set and a
// file→package index for O(1) ResolveString dispatch.
func newTopicConstResolver(modRoot string, pattern string) (*topicConstResolver, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports |
			packages.NeedDeps,
		Dir: modRoot,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return nil, fmt.Errorf("packages.Load: %w", err)
	}
	if errs := flattenPackagesErrors(pkgs); len(errs) > 0 {
		return nil, fmt.Errorf("packages.Load: %d error(s): first=%v", len(errs), errs[0])
	}
	fileIdx := make(map[string]*packages.Package)
	for _, p := range pkgs {
		for _, f := range p.GoFiles {
			fileIdx[f] = p
		}
	}
	return &topicConstResolver{pkgs: pkgs, pkgByFile: fileIdx}, nil
}

// PackageForFile returns the loaded packages.Package owning the given file
// path (absolute). Returns nil if the file is not in the loaded set.
func (r *topicConstResolver) PackageForFile(absPath string) *packages.Package {
	return r.pkgByFile[absPath]
}

// ResolveString attempts to evaluate expr as a string constant. Returns
// (value, true) when go/types confirms expr has a constant string value;
// (zero, false) for non-constant or non-string expressions.
//
// Handles BasicLit, Ident referring to const, SelectorExpr referring to
// const in another package — go/types' built-in folding makes all three
// equivalent at the TypesInfo level.
func (r *topicConstResolver) ResolveString(pkg *packages.Package, expr ast.Expr) (string, bool) {
	if pkg == nil || pkg.TypesInfo == nil {
		return "", false
	}
	tv, ok := pkg.TypesInfo.Types[expr]
	if !ok || tv.Value == nil {
		return "", false
	}
	if tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// flattenPackagesErrors collects all packages.Package.Errors into a flat slice.
func flattenPackagesErrors(pkgs []*packages.Package) []packages.Error {
	var out []packages.Error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		out = append(out, p.Errors...)
	})
	return out
}

// sharedResolverMu guards the module-wide singleton for cells/.
// err paths are NOT cached — if loading fails the next call will retry.
// Only a successful load is cached; this avoids permanently poisoning the
// singleton on transient failures (e.g. missing toolchain during partial CI).
var (
	sharedResolverMu sync.Mutex
	sharedResolver   *topicConstResolver
)

// CellsTopicResolver lazily loads "./cells/..." with full type info and
// returns a shared resolver. Module root is determined by the caller via
// findModuleRoot. All callers that target cells/* should reuse this singleton
// — packages loading is the dominant cost.
//
// Error paths are not cached: a failure on one call allows the next caller
// to retry (e.g. after a missing toolchain becomes available).
func CellsTopicResolver(modRoot string) (*topicConstResolver, error) {
	sharedResolverMu.Lock()
	defer sharedResolverMu.Unlock()
	if sharedResolver != nil {
		return sharedResolver, nil
	}
	r, err := newTopicConstResolver(modRoot, "./cells/...")
	if err != nil {
		return nil, err // do not cache err; next call will retry
	}
	sharedResolver = r
	return r, nil
}
