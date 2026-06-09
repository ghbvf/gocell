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
	"sync"

	"golang.org/x/sync/singleflight"
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
// The cross-module workspace scan ([LoadProductionPackages]) uses the
// ModeWorkspace variant ([loadPackagesMode]); this single-module form is the one
// the Typed / Fixture / StandaloneModule scopes route through, and its signature
// is held stable so the pass / production funnel meta-archtests keep matching it.
func LoadPackages(modRoot string, tests bool, tags []string, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	return loadPackagesMode(packagesload.ModeModule, modRoot, tests, tags, patterns...)
}

// loadPackagesMode is the shared body of LoadPackages; mode selects the GOWORK
// semantics (see tools/packagesload). ModeWorkspace is used only by the
// cross-module workspace production scan.
func loadPackagesMode(
	mode packagesload.Mode, dir string, tests bool, tags []string, patterns ...string,
) ([]*packages.Package, []packages.Error, error) {
	cfg := &packages.Config{
		Mode:  loadMode,
		Dir:   dir,
		Tests: tests,
	}
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	pkgs, err := packagesload.Load(mode, cfg, patterns...)
	if err != nil {
		return nil, nil, fmt.Errorf("packages.Load: %w", err)
	}
	var errs []packages.Error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i := range p.Errors {
			p.Errors[i].Msg = dir + ": " + p.Errors[i].Msg
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
//
// 为什么 cacheKey 保留 patterns 维度而不剥离：archtest 调用方实测分布显示
// 主模块 subpath patterns（./cells/.../, ./cmd/.../, ./runtime/.../ 等）
// 占 83.5%，"./..." 仅占 16.5%。每个 subpath patterns 加载不同 package 集
// 合，必须区分 cacheKey。"./..." 形态的 cacheKey 合并已由
// LoadProductionPackages typed wrapper 完成（固定 patterns="./..."）。
// ref: ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
func SharedResolver(modRoot string, tests bool, tags []string, patterns ...string) (*Resolver, error) {
	return sharedResolverMode(packagesload.ModeModule, modRoot, tests, tags, patterns...)
}

// SharedWorkspaceResolver returns a process-wide cached Resolver using the
// ambient go.work workspace. Use it for bounded typed scans that must cross into
// workspace member modules, such as corecells after the module split.
func SharedWorkspaceResolver(modRoot string, tests bool, tags []string, patterns ...string) (*Resolver, error) {
	return sharedResolverMode(packagesload.ModeWorkspace, modRoot, tests, tags, patterns...)
}

// sharedResolverMode is the mode-parameterized body of SharedResolver and
// SharedWorkspaceResolver. The cache key includes mode so a ModeModule load and
// a ModeWorkspace load of the same (dir, tests, tags, patterns) never alias.
// SharedResolver fixes ModeModule; SharedWorkspaceResolver and
// LoadProductionPackages are the workspace-aware paths.
func sharedResolverMode(mode packagesload.Mode, dir string, tests bool, tags []string, patterns ...string) (*Resolver, error) {
	testsFlag := "0"
	if tests {
		testsFlag = "1"
	}
	key := fmt.Sprintf("%d", mode) + "\x00" + dir + "\x00" + testsFlag + "\x00" +
		strings.Join(tags, "\x00") + "\x00" + strings.Join(patterns, "\x00")

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

		pkgs, errs, err := loadPackagesMode(mode, dir, tests, tags, patterns...)
		if err != nil {
			return nil, err
		}
		if len(errs) > 0 {
			return nil, fmt.Errorf("packages.Load: %d error(s): first=%w", len(errs), errs[0])
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
