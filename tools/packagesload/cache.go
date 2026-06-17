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

// WorkspaceCache memoizes [LoadWorkspace] / [LoadFlat] results across calls
// within a process: repeated identical loads run packages.Load once and reuse
// the loaded packages, collapsing concurrent identical loads via singleflight.
//
// Single source (#2165): this is the ONLY package-load cache in GoCell tooling.
// Both the OBS-01 metric scan (tools/metricschema) and the archtest typed façade
// (tools/archtest/internal/typeseval) route their cached loads here — no package
// re-implements a parallel singleflight+map cache. The loading itself is already
// Hard-funneled by PACKAGES-LOAD-FUNNEL-01 (packages.Load reachable only from
// this package), so a re-introduced parallel cache could not bypass the loader,
// only fail to reuse this cache (a perf regression, not a correctness gap).
//
// Unbounded by design: entries are held for the process lifetime. The consumers
// are short-lived batch processes (`gocell generate`, the archtest gate) that
// load a bounded set of (mode, cfg, patterns) once and exit — exactly the
// in-process warmup pattern this cache exists for. A bounded LRU has no good
// operating point here: a small cap evicts the large production scans and
// triggers re-loads (the regression this fixes), a safe-large cap never evicts.
type WorkspaceCache struct {
	mu    sync.Mutex
	items map[string]cacheEntry
	group singleflight.Group
}

// cacheEntry is a completed, clean load (len(errs)==0) retained for reuse.
type cacheEntry struct {
	pkgs []*packages.Package
	errs []packages.Error
}

// NewWorkspaceCache returns an empty cache. Tests construct isolated instances;
// production routes through the process-wide [LoadWorkspaceCached] /
// [LoadFlatCached].
func NewWorkspaceCache() *WorkspaceCache {
	return &WorkspaceCache{items: map[string]cacheEntry{}}
}

// LoadWorkspace is the cached form of the package-level [LoadWorkspace] (the
// satellite-aware loader). Used by metricschema's OBS-01 scan and typeseval's
// SharedResolver.
func (c *WorkspaceCache) LoadWorkspace(
	root string, cfg packages.Config, patterns ...string,
) ([]*packages.Package, []packages.Error, error) {
	key := cacheKeyFor("W", cfg, root, patterns)
	return c.loadKeyed(key, func() ([]*packages.Package, []packages.Error, error) {
		return LoadWorkspace(root, cfg, patterns...)
	})
}

// LoadFlat is the cached form of a flat ModeWorkspace [Load] from root (no
// satellite parent-prefix expansion) — the cross-module workspace production
// scan typeseval's LoadProductionPackages performs.
func (c *WorkspaceCache) LoadFlat(
	root string, cfg packages.Config, patterns ...string,
) ([]*packages.Package, []packages.Error, error) {
	key := cacheKeyFor("F", cfg, root, patterns)
	return c.loadKeyed(key, func() ([]*packages.Package, []packages.Error, error) {
		return loadFlat(root, cfg, patterns...)
	})
}

// loadFlat performs a flat ModeWorkspace load from root and collects the
// dir-prefixed packages.Errors, mirroring [LoadWorkspace]'s return shape. cfg is
// taken by value; Dir is owned here.
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

// loadKeyed returns the cached result for key, or runs loader once — collapsing
// concurrent identical loads via singleflight — and caches a CLEAN result. A Go
// error or any non-empty packages.Error is NOT cached: both are fail-closed
// failures for callers (metricschema / typeseval both treat non-empty errs as a
// scan failure), so the next call re-loads rather than serving a poisoned entry.
func (c *WorkspaceCache) loadKeyed(
	key string, loader func() ([]*packages.Package, []packages.Error, error),
) ([]*packages.Package, []packages.Error, error) {
	if e, ok := c.get(key); ok {
		return e.pkgs, e.errs, nil
	}
	v, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check: another caller may have filled the cache between our miss
		// and entering Do.
		if e, ok := c.get(key); ok {
			return e, nil
		}
		pkgs, errs, err := loader()
		if err != nil {
			return nil, err
		}
		e := cacheEntry{pkgs: pkgs, errs: errs}
		if len(errs) == 0 {
			c.put(key, e)
		}
		return e, nil
	})
	if err != nil {
		return nil, nil, err
	}
	e := v.(cacheEntry)
	return e.pkgs, e.errs, nil
}

func (c *WorkspaceCache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	return e, ok
}

func (c *WorkspaceCache) put(key string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = e
}

// cacheKeyFor builds the cache key from the result-determining inputs: kind
// ("W" satellite-aware / "F" flat), cfg.Mode, cfg.Tests, cfg.BuildFlags, root,
// and the patterns (sorted — pattern order never changes the loaded package
// SET, and callers dedup/sort downstream). cfg.Context (per-call) and cfg.Dir
// (loader-owned) are deliberately EXCLUDED. NUL separates fields; it cannot
// occur in a filesystem path or go import pattern, so collisions are impossible.
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
