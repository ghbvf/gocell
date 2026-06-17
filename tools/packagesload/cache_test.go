package packagesload

import (
	"context"
	"go/token"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/tools/go/packages"
)

// writeSingleModule writes a minimal single-module (no go.work) tree at a fresh
// temp dir and returns its root. LoadWorkspace takes its ModeModule fallback
// path for such a tree (GOWORK=off appended), so loads are deterministic and
// independent of the ambient repo workspace.
func writeSingleModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeWS(t, root, "go.mod", "module example.com/c\n\ngo 1.25\n")
	writeWS(t, root, "p/p.go", "package p\n\nfunc P() int { return 1 }\n")
	return root
}

// TestWorkspaceCache_Key pins the cache-key semantics WITHOUT a real load: the
// key includes kind / cfg.Mode / cfg.Tests / cfg.BuildFlags / root / sorted
// patterns, and EXCLUDES cfg.Context (per-call) and cfg.Dir (loader-owned).
// Pattern order is normalized (same set → same key) because order never changes
// the loaded package SET.
func TestWorkspaceCache_Key(t *testing.T) {
	root := "/ws/root"
	patterns := []string{"./a/...", "./b/..."}
	base := packages.Config{Mode: packages.NeedName | packages.NeedFiles, Tests: false}
	k := cacheKeyFor("W", base, root, patterns)

	// Context + Dir excluded → same key.
	ctxDir := base
	ctxDir.Context = context.Background()
	ctxDir.Dir = "/some/other/dir"
	if got := cacheKeyFor("W", ctxDir, root, patterns); got != k {
		t.Fatalf("Context/Dir must be excluded from key:\n base=%q\n  got=%q", k, got)
	}

	// Pattern order normalized → same key.
	if got := cacheKeyFor("W", base, root, []string{"./b/...", "./a/..."}); got != k {
		t.Fatalf("pattern order must be normalized in key:\n base=%q\n  got=%q", k, got)
	}

	// Every keyed dimension must change the key.
	diffs := []struct {
		name string
		key  string
	}{
		{"kind W vs F", cacheKeyFor("F", base, root, patterns)},
		{"Mode", cacheKeyFor("W", withMode(base, base.Mode|packages.NeedDeps), root, patterns)},
		{"Tests", cacheKeyFor("W", withTests(base, true), root, patterns)},
		{"BuildFlags", cacheKeyFor("W", withFlags(base, "-tags=x"), root, patterns)},
		{"root", cacheKeyFor("W", base, "/other/root", patterns)},
		{"patterns", cacheKeyFor("W", base, root, []string{"./a/..."})},
	}
	for _, d := range diffs {
		if d.key == k {
			t.Fatalf("%s must change the cache key, but it matched base %q", d.name, k)
		}
	}
}

func withMode(cfg packages.Config, m packages.LoadMode) packages.Config { cfg.Mode = m; return cfg }
func withTests(cfg packages.Config, v bool) packages.Config             { cfg.Tests = v; return cfg }
func withFlags(cfg packages.Config, f string) packages.Config {
	cfg.BuildFlags = []string{f}
	return cfg
}

// TestWorkspaceCache_HitReturnsSamePackages verifies a cache HIT returns the
// identical loaded *packages.Package pointers (no second load) for the same key.
func TestWorkspaceCache_HitReturnsSamePackages(t *testing.T) {
	root := writeSingleModule(t)
	cfg := packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports}
	c := NewWorkspaceCache()

	p1, errs1, err1 := c.LoadWorkspace(root, cfg, "./...")
	if err1 != nil || len(errs1) > 0 {
		t.Fatalf("first LoadWorkspace: err=%v errs=%v", err1, errs1)
	}
	if len(p1) == 0 {
		t.Fatalf("first LoadWorkspace returned no packages")
	}
	p2, _, err2 := c.LoadWorkspace(root, cfg, "./...")
	if err2 != nil {
		t.Fatalf("second LoadWorkspace: err=%v", err2)
	}
	if p1[0] != p2[0] {
		t.Fatalf("cache hit must return the same *packages.Package; got %p vs %p", p1[0], p2[0])
	}
}

// TestWorkspaceCache_ConcurrentSingleflight verifies concurrent cache-miss
// callers of the same key all receive the same loaded packages and the race
// detector stays clean. Without singleflight each goroutine would load
// independently → distinct package pointers and a racy map write; this is the
// regression guard for the singleflight collapse (run under -race).
func TestWorkspaceCache_ConcurrentSingleflight(t *testing.T) {
	root := writeSingleModule(t)
	cfg := packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports}
	c := NewWorkspaceCache()

	const N = 8
	results := make([][]*packages.Package, N)
	errsArr := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			results[i], _, errsArr[i] = c.LoadWorkspace(root, cfg, "./...")
		}()
	}
	wg.Wait()

	for i, err := range errsArr {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if len(results[i]) == 0 {
			t.Fatalf("goroutine %d got no packages", i)
		}
	}
	for i := 1; i < N; i++ {
		if results[i][0] != results[0][0] {
			t.Fatalf("goroutine %d got a distinct *packages.Package (singleflight regressed): %p vs %p",
				i, results[i][0], results[0][0])
		}
	}
}

// TestWorkspaceCache_FailureNotCached verifies a load that surfaces
// packages.Error is NOT cached: a second call re-loads (fresh package pointers)
// rather than serving a poisoned entry. Both callers see the errors and decide
// to fail closed.
func TestWorkspaceCache_FailureNotCached(t *testing.T) {
	root := t.TempDir()
	writeWS(t, root, "go.mod", "module example.com/bad\n\ngo 1.25\n")
	writeWS(t, root, "bad/bad.go", "package bad\n\nfunc F() { undefinedSymbol() }\n")
	cfg := packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
		packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports}
	c := NewWorkspaceCache()

	p1, errs1, err1 := c.LoadWorkspace(root, cfg, "./bad/...")
	if err1 != nil {
		t.Fatalf("loader itself should not error: %v", err1)
	}
	if len(errs1) == 0 {
		t.Fatalf("type error must surface as packages.Error")
	}
	if len(p1) == 0 {
		t.Fatalf("bad package should still load (with errors)")
	}

	p2, errs2, err2 := c.LoadWorkspace(root, cfg, "./bad/...")
	if err2 != nil || len(errs2) == 0 {
		t.Fatalf("second call must re-surface errors (not cached): err=%v errs=%v", err2, errs2)
	}
	if p1[0] == p2[0] {
		t.Fatalf("failed load must not be cached — second call must re-load (distinct package pointer)")
	}
}

// TestWorkspaceCache_LoadFlatCaches verifies the flat ModeWorkspace path caches
// and is keyed distinctly from LoadWorkspace (kind "F" vs "W").
func TestWorkspaceCache_LoadFlatCaches(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "off")
	writeWS(t, root, "go.work", "go 1.25\n\nuse .\n")
	writeWS(t, root, "go.mod", "module example.com/flat\n\ngo 1.25\n")
	writeWS(t, root, "p/p.go", "package p\n\nfunc P() int { return 1 }\n")
	cfg := packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports}
	c := NewWorkspaceCache()

	p1, errs1, err1 := c.LoadFlat(root, cfg, "./p/...")
	if err1 != nil || len(errs1) > 0 {
		t.Fatalf("first LoadFlat: err=%v errs=%v", err1, errs1)
	}
	if !containsPkgPath(p1, "example.com/flat/p") {
		t.Fatalf("LoadFlat did not load example.com/flat/p; got %v", pkgPaths(p1))
	}
	p2, _, err2 := c.LoadFlat(root, cfg, "./p/...")
	if err2 != nil {
		t.Fatalf("second LoadFlat: err=%v", err2)
	}
	if p1[0] != p2[0] {
		t.Fatalf("LoadFlat cache hit must return the same *packages.Package; got %p vs %p", p1[0], p2[0])
	}
}

// TestLoadWorkspaceCached_SharedInstance verifies the package-level cached entry
// points reuse one process-wide cache: two calls with the same key serve the
// same loaded packages.
func TestLoadWorkspaceCached_SharedInstance(t *testing.T) {
	root := writeSingleModule(t)
	cfg := packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports}

	p1, errs1, err1 := LoadWorkspaceCached(root, cfg, "./...")
	if err1 != nil || len(errs1) > 0 {
		t.Fatalf("first LoadWorkspaceCached: err=%v errs=%v", err1, errs1)
	}
	if len(p1) == 0 {
		t.Fatalf("no packages loaded")
	}
	p2, _, err2 := LoadWorkspaceCached(root, cfg, "./...")
	if err2 != nil {
		t.Fatalf("second LoadWorkspaceCached: err=%v", err2)
	}
	if p1[0] != p2[0] {
		t.Fatalf("shared cache must reuse loaded packages across calls; got %p vs %p", p1[0], p2[0])
	}
}

// TestWorkspaceCache_RejectsUncacheableConfig verifies the cached entrypoints
// fail fast (both LoadWorkspace and LoadFlat, via the loadKeyed funnel) when cfg
// sets a result-affecting field that is NOT in the cache key — otherwise the
// second call could be served a stale package graph.
func TestWorkspaceCache_RejectsUncacheableConfig(t *testing.T) {
	root := writeSingleModule(t)
	c := NewWorkspaceCache()
	cases := []struct {
		name string
		cfg  packages.Config
	}{
		{"Env", packages.Config{Mode: packages.NeedName, Env: []string{"FOO=bar"}}},
		{"Overlay", packages.Config{Mode: packages.NeedName, Overlay: map[string][]byte{"/x.go": {}}}},
		{"Fset", packages.Config{Mode: packages.NeedName, Fset: token.NewFileSet()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.LoadWorkspace(root, tc.cfg, "./..."); err == nil {
				t.Fatalf("LoadWorkspace must fail-fast when cfg.%s is set (unkeyed, affects load result)", tc.name)
			}
			if _, _, err := c.LoadFlat(root, tc.cfg, "./..."); err == nil {
				t.Fatalf("LoadFlat must fail-fast when cfg.%s is set (same loadKeyed funnel)", tc.name)
			}
		})
	}
}

// TestWorkspaceCache_loadKeyed_RunsLoaderOnce is the deterministic singleflight
// proof: N concurrent callers of the same key must collapse to exactly one
// loader execution (atomic counter == 1) and all receive the same packages. A
// blocking fake loader holds the single in-flight load open while the others
// pile in; if singleflight regressed, each cache-miss caller would run its own
// loader and the counter would exceed 1.
func TestWorkspaceCache_loadKeyed_RunsLoaderOnce(t *testing.T) {
	c := NewWorkspaceCache()
	cfg := packages.Config{Mode: packages.NeedName}
	const N = 8
	var calls atomic.Int64
	entered := make(chan struct{}, N)
	proceed := make(chan struct{})
	//nolint:unparam // signature is dictated by loadKeyed's loader param; this fake always succeeds
	loader := func() ([]*packages.Package, []packages.Error, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-proceed // hold the in-flight load open so concurrent callers join it
		return []*packages.Package{{PkgPath: "example.com/x"}}, nil, nil
	}

	results := make([][]*packages.Package, N)
	var ready, done sync.WaitGroup
	ready.Add(N)
	done.Add(N)
	for i := range N {
		go func() {
			defer done.Done()
			ready.Done()
			results[i], _, _ = c.loadKeyed(cacheKindWorkspace, "/r", cfg, []string{"./..."}, loader)
		}()
	}
	ready.Wait() // all goroutines launched
	<-entered    // the single in-flight load has started (blocked on proceed)
	close(proceed)
	done.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("singleflight must run the loader exactly once per key; ran %d times", got)
	}
	for i := 1; i < N; i++ {
		if results[i][0] != results[0][0] {
			t.Fatalf("all callers must receive the same loaded packages; goroutine %d differs", i)
		}
	}
}

// TestLoadFlatCached_SharedInstance proves the flat production warm-up path
// (LoadProductionPackages -> LoadFlatCached) reuses the process-wide cache: a
// repeated LoadFlatCached of the same (root, patterns) returns the same
// *packages.Package pointers (the F3 coverage gap for the kind "F" path).
func TestLoadFlatCached_SharedInstance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "off")
	writeWS(t, root, "go.work", "go 1.25\n\nuse .\n")
	writeWS(t, root, "go.mod", "module example.com/flatshared\n\ngo 1.25\n")
	writeWS(t, root, "p/p.go", "package p\n\nfunc P() int { return 1 }\n")
	cfg := packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports}

	p1, errs1, err1 := LoadFlatCached(root, cfg, "./p/...")
	if err1 != nil || len(errs1) > 0 {
		t.Fatalf("first LoadFlatCached: err=%v errs=%v", err1, errs1)
	}
	if !containsPkgPath(p1, "example.com/flatshared/p") {
		t.Fatalf("LoadFlatCached did not load example.com/flatshared/p; got %v", pkgPaths(p1))
	}
	p2, _, err2 := LoadFlatCached(root, cfg, "./p/...")
	if err2 != nil {
		t.Fatalf("second LoadFlatCached: err=%v", err2)
	}
	if p1[0] != p2[0] {
		t.Fatalf("LoadFlatCached must reuse the process-wide cache (flat production path); got %p vs %p", p1[0], p2[0])
	}
}
