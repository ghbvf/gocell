package packagesload

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
	"golang.org/x/tools/go/packages"
)

// cache-key kind discriminators: which loader produced (and would re-serve) the
// entry. A "W" entry and an "F" entry for the same (root, cfg, patterns) load
// different package sets, so they never alias.
const (
	cacheKindWorkspace = "W" // satellite-aware LoadWorkspace
	cacheKindFlat      = "F" // flat ModeWorkspace LoadFlat
)

// WorkspaceCache memoizes [LoadWorkspace] / [LoadFlat] results across calls
// within a process: repeated identical loads run packages.Load once and reuse
// the loaded packages, collapsing concurrent identical loads via singleflight.
//
// Single source (#2165): this is the sanctioned single package-load cache in
// GoCell tooling. Both the OBS-01 metric scan (tools/metricschema) and the
// archtest typed façade (tools/archtest/internal/typeseval) route their cached
// loads here, rather than each re-implementing a parallel singleflight+map cache.
// The loading itself is Hard-funneled by PACKAGES-LOAD-FUNNEL-01 (packages.Load
// reachable only from this package), so a re-introduced parallel cache could not
// bypass the loader (Hard) — it could only fail to reuse this cache (a perf
// regression, not a correctness gap). The "single cache" boundary is documented,
// not separately enforced.
//
// Unbounded by design: entries are held for the process lifetime. The consumers
// are short-lived batch processes (`gocell generate`, the archtest gate) that
// load a bounded set of (mode, cfg, patterns) once and exit — exactly the
// in-process warmup pattern this cache exists for. A bounded LRU has no good
// operating point here: a small cap evicts the large production scans and
// triggers re-loads (the regression this fixes), a safe-large cap never evicts.
type WorkspaceCache struct {
	mu    sync.Mutex
	items map[string][]*packages.Package // CLEAN loads only (len(errs)==0); never an errored result
	group singleflight.Group
}

// loadResult is the transient carrier passed through singleflight so co-flight
// waiters see the same (pkgs, errs). Only the pkgs of a CLEAN load reach the
// items map — errs never persist, so an errored load can never be served from
// cache (type-level fail-closed).
type loadResult struct {
	pkgs []*packages.Package
	errs []packages.Error
}

// NewWorkspaceCache returns an empty cache. Tests construct isolated instances;
// production routes through the process-wide [LoadWorkspaceCached] /
// [LoadFlatCached].
func NewWorkspaceCache() *WorkspaceCache {
	return &WorkspaceCache{items: map[string][]*packages.Package{}}
}

// LoadWorkspace is the cached form of the package-level [LoadWorkspace] (the
// satellite-aware loader). Used by metricschema's OBS-01 scan and typeseval's
// SharedResolver.
func (c *WorkspaceCache) LoadWorkspace(
	root string, cfg packages.Config, patterns ...string,
) ([]*packages.Package, []packages.Error, error) {
	return c.loadKeyed(cacheKindWorkspace, root, cfg, patterns, func() ([]*packages.Package, []packages.Error, error) {
		return LoadWorkspace(root, cfg, patterns...)
	})
}

// LoadFlat is the cached form of a flat ModeWorkspace [Load] from root (no
// satellite parent-prefix expansion) — the cross-module workspace production
// scan typeseval's LoadProductionPackages performs.
//
// Keyed distinctly from [WorkspaceCache.LoadWorkspace] (kind "F" vs "W"): the
// two load different package sets, so a warm-up via LoadFlat does NOT pre-heat
// LoadWorkspace callers (SharedResolver) and vice versa — each holds its own entry.
func (c *WorkspaceCache) LoadFlat(
	root string, cfg packages.Config, patterns ...string,
) ([]*packages.Package, []packages.Error, error) {
	return c.loadKeyed(cacheKindFlat, root, cfg, patterns, func() ([]*packages.Package, []packages.Error, error) {
		return loadFlat(root, cfg, patterns...)
	})
}

// loadFlat performs a flat ModeWorkspace load from root and collects the
// dir-prefixed packages.Errors, mirroring [LoadWorkspace]'s return shape (the
// prefix is the flat root, matching the pre-#2165 typeseval ModeWorkspace path).
// cfg is taken by value; Dir is owned here.
func loadFlat(root string, cfg packages.Config, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	cfg.Dir = root
	pkgs, err := Load(ModeWorkspace, &cfg, patterns...)
	if err != nil {
		return nil, nil, fmt.Errorf("packages.Load: %w", err)
	}
	var errs []packages.Error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i := range p.Errors {
			p.Errors[i].Msg = root + ": " + p.Errors[i].Msg
		}
		errs = append(errs, p.Errors...)
	})
	return pkgs, errs, nil
}

// validateCacheable fails fast when cfg sets a field that affects the load
// RESULT but is NOT part of the cache key — otherwise two calls with identical
// keyed fields (Mode / Tests / BuildFlags / root / patterns) but different
// Env / Overlay / ParseFile / Fset would alias, and the second would be served a
// stale package graph (wrong build env, file contents, AST, or positions).
// Context (per-call) and Dir (loader-owned) are intentionally unkeyed and thus
// allowed; Logf is diagnostic-only. The uncached package-level [LoadWorkspace] /
// [Load] impose no such restriction — a caller needing these fields uses them.
func validateCacheable(cfg packages.Config) error {
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"Env", cfg.Env != nil},
		{"Overlay", cfg.Overlay != nil},
		{"ParseFile", cfg.ParseFile != nil},
		{"Fset", cfg.Fset != nil},
	} {
		if f.set {
			return fmt.Errorf("packagesload: cached load cannot key on cfg.%s "+
				"(it affects the load result but is not in the cache key); "+
				"leave it nil or use the uncached loader", f.name)
		}
	}
	return nil
}

// loadKeyed is the single funnel for every cached load: it validates cfg
// ([validateCacheable]), computes the key ([cacheKeyFor]), then returns the
// cached packages or runs loader once — collapsing concurrent identical loads
// via singleflight — and caches only a CLEAN result. Routing both validation and
// keying through here makes them unavoidable for any cached entrypoint.
//
// A Go error or any non-empty packages.Error is NOT persisted: both are
// fail-closed failures for callers (metricschema / typeseval both treat non-empty
// errs as a scan failure), so the NEXT independent call re-loads rather than
// serving a poisoned entry. Concurrent co-flight waiters of a failed load share
// that one failure result; only persistence is suppressed.
//
// On a cache HIT no load runs, so the caller's cfg.Context is irrelevant — a
// canceled ctx does not interrupt a hit; ctx only governs the first real load.
// The singleflight "shared" bool is intentionally discarded: a build-time batch
// tool has no need to distinguish a self-load from a collapsed one.
func (c *WorkspaceCache) loadKeyed(
	kind, root string, cfg packages.Config, patterns []string,
	loader func() ([]*packages.Package, []packages.Error, error),
) ([]*packages.Package, []packages.Error, error) {
	if err := validateCacheable(cfg); err != nil {
		return nil, nil, err
	}
	key := cacheKeyFor(kind, cfg, root, patterns)
	if pkgs, ok := c.get(key); ok {
		return pkgs, nil, nil
	}
	v, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check: another caller may have filled the cache between our miss
		// and entering Do.
		if pkgs, ok := c.get(key); ok {
			return loadResult{pkgs: pkgs}, nil
		}
		pkgs, errs, err := loader()
		if err != nil {
			return nil, err
		}
		if len(errs) == 0 {
			c.put(key, pkgs)
		}
		return loadResult{pkgs: pkgs, errs: errs}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	r := v.(loadResult)
	return r.pkgs, r.errs, nil
}

func (c *WorkspaceCache) get(key string) ([]*packages.Package, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pkgs, ok := c.items[key]
	return pkgs, ok
}

func (c *WorkspaceCache) put(key string, pkgs []*packages.Package) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = pkgs
}

// cacheKeyFor builds the cache key from the result-determining inputs: kind
// ([cacheKindWorkspace] "W" satellite-aware / [cacheKindFlat] "F" flat),
// cfg.Mode, cfg.Tests, cfg.BuildFlags, root, and the patterns (sorted — pattern
// order never changes the loaded package SET, and callers dedup/sort downstream).
// cfg.Context (per-call) and cfg.Dir (loader-owned) are deliberately EXCLUDED;
// because Context is not keyed, a cache hit ignores a canceled ctx (see
// [WorkspaceCache.loadKeyed]). NUL separates fields; it cannot occur in a
// filesystem path or go import pattern, so collisions are impossible.
func cacheKeyFor(kind string, cfg packages.Config, root string, patterns []string) string {
	tests := "0"
	if cfg.Tests {
		tests = "1"
	}
	pats := append([]string(nil), patterns...)
	sort.Strings(pats)
	return strings.Join([]string{
		kind,
		strconv.Itoa(int(cfg.Mode)),
		tests,
		strings.Join(cfg.BuildFlags, "\x00"),
		root,
		strings.Join(pats, "\x00"),
	}, "\x00")
}

// sharedWorkspaceCache is the sanctioned process-wide instance both consumers
// route through; they hold no cache state of their own.
var sharedWorkspaceCache = NewWorkspaceCache()

// LoadWorkspaceCached is [WorkspaceCache.LoadWorkspace] on the process-wide cache.
func LoadWorkspaceCached(root string, cfg packages.Config, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	return sharedWorkspaceCache.LoadWorkspace(root, cfg, patterns...)
}

// LoadFlatCached is [WorkspaceCache.LoadFlat] on the process-wide cache.
func LoadFlatCached(root string, cfg packages.Config, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	return sharedWorkspaceCache.LoadFlat(root, cfg, patterns...)
}
