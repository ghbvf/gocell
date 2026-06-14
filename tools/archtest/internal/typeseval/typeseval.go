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
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/packagesload"
	"github.com/ghbvf/gocell/tools/workspace"
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
	if mode == packagesload.ModeModule {
		if groups, rootPatterns, ok := workspacePatternGroups(dir, patterns); ok {
			// A span across multiple member modules, OR a single group that IS the
			// workspace root (parent-of-member / root-level patterns), must load
			// from the root in ModeWorkspace using rootPatterns — where unanchored
			// "./parent/..." patterns have been translated to import-path form
			// (post-#1565 the root has no module to anchor a relative dir pattern).
			if len(groups) > 1 || (len(groups) == 1 && groups[0].dir == dir) {
				return loadPackageGroups(
					packagesload.ModeWorkspace,
					tests,
					tags,
					[]patternGroup{{dir: dir, patterns: rootPatterns}},
				)
			}
			return loadPackageGroups(mode, tests, tags, groups)
		}
	}
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

type patternGroup struct {
	dir      string
	patterns []string
}

func loadPackageGroups(
	mode packagesload.Mode, tests bool, tags []string, groups []patternGroup,
) ([]*packages.Package, []packages.Error, error) {
	var all []*packages.Package
	var allErrs []packages.Error
	for _, g := range groups {
		cfg := &packages.Config{
			Mode:  loadMode,
			Dir:   g.dir,
			Tests: tests,
		}
		if len(tags) > 0 {
			cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
		}
		pkgs, err := packagesload.Load(mode, cfg, g.patterns...)
		if err != nil {
			return nil, nil, fmt.Errorf("packages.Load: %w", err)
		}
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			for i := range p.Errors {
				p.Errors[i].Msg = g.dir + ": " + p.Errors[i].Msg
			}
			allErrs = append(allErrs, p.Errors...)
		})
		all = append(all, pkgs...)
	}
	return all, allErrs, nil
}

// workspacePatternGroups returns, for a go.work workspace at root: (1) the
// per-member pattern groups (each loaded in module mode), and (2) rootPatterns —
// the equivalent flat pattern list for a single ModeWorkspace load FROM the root,
// in which any "./parent/..." pattern that spans multiple members (no single
// member owns it) is rewritten to its import-path form so workspace mode resolves
// it without a root module to anchor the relative dir.
func workspacePatternGroups(root string, patterns []string) ([]patternGroup, []string, bool) {
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		return nil, nil, false
	}
	mods, err := workspace.Modules(root)
	if err != nil {
		return nil, nil, false
	}
	// Expand satellite parent-prefixes ("./cmd/...", "./adapters/...",
	// "./examples/...") that span multiple members into their per-member patterns
	// so the satellite packages are ACTUALLY scanned. Without this a pattern no
	// single member owns hits splitWorkspacePattern's skip and match-zeroes —
	// silently dropping the ./cmd/… ./adapters/… ./examples/… coverage that
	// security/governance archtest rules declare over those satellites (#1565
	// fidelity, F5). A prefix with zero members under it still falls through to
	// the skip (genuine match-zero).
	expanded := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if parts, ok := workspace.ExpandParentPrefix(mods, p); ok {
			expanded = append(expanded, parts...)
		} else {
			expanded = append(expanded, p)
		}
	}
	patterns = expanded
	groups := make([]patternGroup, 0, len(mods))
	groupByDir := map[string]int{}
	add := func(dir, pattern string) {
		if i, ok := groupByDir[dir]; ok {
			groups[i].patterns = append(groups[i].patterns, pattern)
			return
		}
		groupByDir[dir] = len(groups)
		groups = append(groups, patternGroup{dir: dir, patterns: []string{pattern}})
	}
	rootPatterns := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		moduleDir, modulePattern := splitWorkspacePattern(mods, pattern)
		if moduleDir == skipPatternDir {
			// Unmatched satellite parent-prefix ("./cmd/...", "./adapters/...") —
			// drop it so it match-zeroes (see splitWorkspacePattern).
			continue
		}
		add(filepath.Join(root, moduleDir), modulePattern)
		if moduleDir == "." {
			// No member owns the pattern: use the member-relative form for the
			// root-anchored ModeWorkspace load.
			rootPatterns = append(rootPatterns, modulePattern)
		} else {
			// Into-member relative pattern resolves from the root in workspace mode.
			rootPatterns = append(rootPatterns, pattern)
		}
	}
	return groups, rootPatterns, true
}

// frameworkModuleDir is the workspace-relative dir of the core framework module
// (kernel/runtime/pkg) since the #1565 split — the successor to the pre-split root
// module that "." / "./..." referred to.
const frameworkModuleDir = "framework"

// skipPatternDir is the sentinel member-dir splitWorkspacePattern returns for a
// root-relative parent prefix that no workspace member owns AND that has no member
// living under it — a genuine empty match (e.g. a typo "./nonexistent/..."). Real
// multi-member satellite prefixes ("./cmd/...", "./adapters/...", "./examples/...")
// are expanded to their members by workspacePatternGroups BEFORE split is called
// (workspace.ExpandParentPrefix), so they are actually scanned, never silently
// skipped (F5, SATELLITE-PARENT-PREFIX-SCAN-01). workspacePatternGroups drops a
// skipPatternDir pattern so it match-zeroes. The NUL byte cannot occur in a real
// dir, so it is unambiguous.
const skipPatternDir = "\x00skip-no-member"

func splitWorkspacePattern(mods []workspace.Module, pattern string) (string, string) {
	// "." / "./..." referred to the pre-#1565 ROOT module — the platform CORE
	// (kernel/runtime/pkg at the repo root). The core moved to ./framework, so map
	// the bare-root patterns onto the framework member, preserving "scan the
	// platform core" semantics.
	if (pattern == "." || pattern == "./...") && hasFrameworkMember(mods) {
		return frameworkModuleDir, pattern
	}
	if dir, modulePattern, ok := matchWorkspaceMember(mods, pattern); ok {
		return dir, modulePattern
	}
	// No member matched a root-relative parent prefix. Multi-member satellite
	// prefixes ("./cmd/...", "./adapters/...", "./examples/...") are already expanded
	// to their owning members upstream in workspacePatternGroups
	// (workspace.ExpandParentPrefix), so they are scanned, not skipped (F5). Reaching
	// here means the prefix has NO member under it at all — a genuine empty match
	// (e.g. a typo "./nonexistent/..."). Signal SKIP: workspacePatternGroups drops the
	// pattern entirely (→ match-zero). A member that matched but whose subdir is
	// missing (e.g. "./tools/.../nonexistent/...") took the matched branch above and is
	// NOT skipped — it loads and surfaces a packages.Error, preserving typo
	// diagnostics. Only skip when a framework core member exists (the real post-#1565
	// workspace); a synthetic single-module set falls through to the root-anchored
	// (".") form so its in-module subtrees still load.
	if strings.HasPrefix(pattern, "./") && hasFrameworkMember(mods) {
		return skipPatternDir, ""
	}
	return ".", pattern
}

// hasFrameworkMember reports whether the workspace contains the core framework
// module (the post-#1565 successor to the root "." module).
func hasFrameworkMember(mods []workspace.Module) bool {
	for _, m := range mods {
		if filepath.ToSlash(filepath.Clean(m.Dir)) == frameworkModuleDir {
			return true
		}
	}
	return false
}

// matchWorkspaceMember finds the workspace member that owns pattern (longest dir /
// import-path prefix wins), returning (member-dir, member-relative pattern, true).
// ok is false when no member owns the pattern.
func matchWorkspaceMember(mods []workspace.Module, pattern string) (string, string, bool) {
	bestDir, bestPattern, bestScore := "", "", -1
	consider := func(score int, dir, modulePattern string) {
		if score > bestScore {
			bestScore, bestDir, bestPattern = score, dir, modulePattern
		}
	}
	for _, m := range mods {
		dir := filepath.ToSlash(filepath.Clean(m.Dir))
		if dir == "." || dir == "" {
			continue
		}
		prefix := "./" + dir
		switch {
		case pattern == prefix:
			consider(len(dir), dir, ".")
		case strings.HasPrefix(pattern, prefix+"/"):
			consider(len(dir), dir, "."+strings.TrimPrefix(pattern, prefix))
		}
		if pattern == m.ImportPath || strings.HasPrefix(pattern, m.ImportPath+"/") {
			consider(len(m.ImportPath), dir, pattern)
		}
	}
	return bestDir, bestPattern, bestScore >= 0
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

// sharedResolverMode is the mode-parameterized body of SharedResolver. The cache
// key includes mode so a ModeModule load and a ModeWorkspace load of the same
// (dir, tests, tags, patterns) never alias. SharedResolver fixes ModeModule
// (the only public form, kept stable for the funnel meta-archtests); the
// ModeWorkspace path is reached solely via LoadProductionPackages for the
// cross-module workspace production scan.
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
