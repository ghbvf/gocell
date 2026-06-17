// Package typeseval provides go/types-backed helpers for archtest scanners.
//
// Scope: archtest internal helper. Not exported beyond tools/archtest because
// kernel/governance enforces stdlib-only and runtime/cells/adapters have no
// reason to evaluate AST constants.
//
// The helpers cover three patterns:
//
//  1. EvaluateConstString — collapse BasicLit / Ident / SelectorExpr / BinaryExpr
//     to their compile-time string constant value via go/types' built-in
//     constant folding.
//  2. LoadPackages / SharedResolver — load a module subtree with full type info
//     once, then resolve any *ast.Expr to its constant via the owning
//     packages.Package. Both accept a `tests` flag (true loads test variant
//     packages, including *_test.go files) and a `tags` slice (joined into
//     -tags=a,b,c BuildFlags).
//  3. ResolveMethodCall / ResolvePackageRef / ResolveEnclosingFunc — given a
//     SelectorExpr / Expr / Node, return the canonical *types.Func or
//     (pkg, symbol) identity for callee-side and caller-side checks.
//     ResolveEnclosingFunc is the caller-side helper: walk a file's top-level
//     FuncDecls and return the one that lexically contains the node, mapped to
//     its *types.Func (used as callsite identity for funnel allowlists).
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

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/packagesload"
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

// loadMode is the go/packages load mode for every archtest typed scope
// (Typed / Production / Fixture / StandaloneModule all route through here via
// SharedResolver).
//
// NeedDeps is deliberately OMITTED (#1499). It would keep full Syntax(AST) +
// TypesInfo resident for every transitive dependency package (~1135 of them) —
// the dominant RSS increment of a single packages.Load (measured HeapAlloc
// 954MB→149MB tests=F, 1337MB→509MB tests=T). Without NeedDeps, go/packages
// builds dependency *types.Package values from export data (gcexportdata,
// lightweight Types only) via its usesExportData fast path (NeedTypes &&
// !NeedDeps); this is exactly go/analysis' LoadSyntax preset. The root package's
// Types / TypesInfo / Syntax are unaffected, and cross-package symbol resolution
// (info.Uses, types.Implements, Scope().Lookup) is identical to NeedDeps.
//
// Do NOT re-add NeedDeps "to be safe". Its ONLY behavioral effect here is to
// prune the transitive (*types.Package).Imports() closure to type-referenced
// packages. The archtest transitive-walk rules are sound under that pruning
// because producing a finding via a NAMED-TYPE reference requires the root to
// type-reference the target package (so the target stays a direct import and
// survives pruning). The cell raw-option rule's types.Implements structural-match
// (an anonymous interface matching a forbidden method set need not name the
// forbidden package) is the one path not covered by that argument; it is verified
// by the funnel smokes in ../../loadmode_nodeps_invariants_test.go plus the
// one-time full NoDeps-vs-WithDeps comparison performed when #1499 landed (see that
// file's package doc for why a permanent differential is intentionally not added).
// Guarded by TestLoadMode_NoNeedDeps.
//
// ref: golang/tools go/packages/packages.go usesExportData + NeedDeps doc
// ref: golang/tools go/analysis LoadSyntax (no NeedDeps) — root-only analysis
const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
	packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports

// LoadPackages loads patterns from modRoot with full type info in single-module
// mode (GOWORK=off): archtest analyzes the root module plus the isolated fixture
// modules under tools/archtest/testdata/*, none of which are in the repo go.work
// `use` set.
//
// Parameters:
//   - tests: when true, load the test variant of each package (includes
//     *_test.go and adds a synthetic xtest package for `package x_test`).
//   - tags: joined as `-tags=a,b,c` in BuildFlags; pass nil/empty to omit.
//
// Returns the flat slice of packages.Errors collected from every package as
// the second value so callers can fail fast on type-check errors without
// re-walking.
//
// This is the UNCACHED form, delegating to the shared satellite-aware loader
// packagesload.LoadWorkspace, so a satellite parent prefix ("./cmd/...",
// "./adapters/...", "./examples/...") is expanded to its go.work members and
// loaded in workspace mode — the single-module name refers to the GOWORK mode
// requested, not a guarantee that only one module loads. The signature is held
// stable so the pass / production funnel meta-archtests keep matching it.
//
// Prefer [SharedResolver] (the cached counterpart, #2165) in new code; this
// uncached form exists only for that signature-stability requirement — a fresh
// caller that wants memoization must not reach for LoadPackages.
func LoadPackages(modRoot string, tests bool, tags []string, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	return packagesload.LoadWorkspace(modRoot, typesevalCfg(tests, tags), patterns...)
}

// typesevalCfg builds the packages.Config shared by every archtest typed scope:
// the #1499 no-NeedDeps loadMode plus the tests flag and optional -tags. Those
// fields (Mode/Tests/BuildFlags) are exactly the ones the packagesload cache
// keys on, so two loads with the same (tests, tags, patterns) share a cache entry.
func typesevalCfg(tests bool, tags []string) packages.Config {
	cfg := packages.Config{Mode: loadMode, Tests: tests}
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	return cfg
}

// SharedResolver returns a Resolver backed by the process-wide packagesload
// cache (#2165), keyed on (modRoot, tests, tags, patterns). Successive callers
// with the same key reuse the satellite-aware cached load instead of re-running
// packages.Load. Errors are not cached, so a transient failure does not poison
// subsequent calls. The returned *Resolver is a thin wrapper minted per call;
// two calls share the same underlying packages on a cache hit (the property
// that matters: no re-load), not the same wrapper.
//
// The cache lives in tools/packagesload (the single sanctioned package-load
// cache); typeseval holds no cache state of its own. SharedResolver wraps the
// raw cached load [packagesload.LoadWorkspaceCached], mapping any packages.Error
// into a fail-fast scan error via [resolverFrom].
// ref: ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
func SharedResolver(modRoot string, tests bool, tags []string, patterns ...string) (*Resolver, error) {
	return resolverFrom(packagesload.LoadWorkspaceCached(modRoot, typesevalCfg(tests, tags), patterns...))
}

// resolverFrom wraps a (cached) load result into a *Resolver, mapping a Go load
// error or any non-empty packages.Error into a fail-fast error (a partial load
// is a scan failure). Shared by SharedResolver and LoadProductionPackages.
func resolverFrom(pkgs []*packages.Package, errs []packages.Error, err error) (*Resolver, error) {
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("packages.Load: %d error(s): first=%w", len(errs), errs[0])
	}
	return &Resolver{pkgs: pkgs}, nil
}
