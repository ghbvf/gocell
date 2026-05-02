package depgraph_test

import (
	"sort"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/depgraph"
)

func TestTransitiveImports_DAG(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)

	// a → b → c, a → d. closure of a = {b, c, d}.
	got := g.TransitiveImports(synthModule + "/a")
	want := []string{
		synthModule + "/b",
		synthModule + "/c",
		synthModule + "/d",
	}
	gotSlice := keys(got)
	sort.Strings(gotSlice)
	sort.Strings(want)
	if !equalStrings(gotSlice, want) {
		t.Errorf("TransitiveImports(a) = %v, want %v", gotSlice, want)
	}
}

func TestTransitiveImports_Leaf(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	got := g.TransitiveImports(synthModule + "/c")
	if len(got) != 0 {
		t.Errorf("TransitiveImports(c) = %v, want empty (leaf)", got)
	}
}

func TestTransitiveImports_MissingNode(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	got := g.TransitiveImports("does.not/exist")
	if len(got) != 0 {
		t.Errorf("TransitiveImports(missing) = %v, want empty", got)
	}
}

func TestTransitiveImports_StaysInModule(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, true) // include tests so test variant edges exist
	for _, id := range []string{
		synthModule + "/a",
		synthModule + "/b",
		synthModule + "/c",
	} {
		closure := g.TransitiveImports(id)
		for dep := range closure {
			if dep != synthModule && !startsWith(dep, synthModule+"/") {
				t.Errorf("closure(%s) includes %s; should stay inside module %s",
					id, dep, synthModule)
			}
		}
	}
	// Spot-check: stdlib (e.g. "testing") must never appear in any closure.
	for _, n := range g.Packages {
		closure := g.TransitiveImports(n.ID)
		for dep := range closure {
			if dep == "testing" || dep == "fmt" || dep == "encoding/json" {
				t.Errorf("closure(%s) leaked stdlib package %s", n.ID, dep)
			}
		}
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestTransitiveImports_ExcludesTestOnly(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, true)
	closure := g.TransitiveImports(synthModule + "/a")
	if closure[synthModule+"/testhelper"] {
		t.Errorf("closure(a) includes testhelper; testOnly nodes should be excluded")
	}
}

// TestTransitiveImports_FreshCopy proves the contract that each call returns
// an independent map: mutating the first result must not leak into a second
// call. Without this guarantee, archtest's per-rule iteration could pollute
// the closure for sibling rules.
func TestTransitiveImports_FreshCopy(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	first := g.TransitiveImports(synthModule + "/a")
	if len(first) == 0 {
		t.Fatal("expected non-empty closure for /a")
	}

	// Snapshot the keys so we can detect any drift after mutation.
	snapshot := keys(first)
	sort.Strings(snapshot)

	// Mutate the first result aggressively.
	for k := range first {
		delete(first, k)
	}
	first["bogus"] = true

	// Second call must reproduce the original closure independently.
	second := g.TransitiveImports(synthModule + "/a")
	gotSlice := keys(second)
	sort.Strings(gotSlice)
	if !equalStrings(gotSlice, snapshot) {
		t.Errorf("second call returned %v, want %v (mutation of first leaked)", gotSlice, snapshot)
	}
	if second["bogus"] {
		t.Errorf("second call contains injected key; results not independent")
	}
}

// TestTransitiveImportsWithPaths_DAG locks the path-recording contract:
// each reachable dep is keyed to a chain that starts at the source and
// ends at the dep, inclusive on both ends. Used by archtest's LAYER-05T/
// 06T/09T violation messages to render "src → util → dep".
func TestTransitiveImportsWithPaths_DAG(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	src := synthModule + "/a"
	paths := g.TransitiveImportsWithPaths(src)

	// a → b → c, a → d (synth fixture). Each path is anchored at src
	// and terminates at the dep; b and d are direct, c is transitive.
	for _, dep := range []string{
		synthModule + "/b",
		synthModule + "/c",
		synthModule + "/d",
	} {
		path, ok := paths[dep]
		if !ok {
			t.Errorf("missing path entry for %s", dep)
			continue
		}
		if len(path) < 2 {
			t.Errorf("path to %s too short: %v", dep, path)
			continue
		}
		if path[0] != src {
			t.Errorf("path[0] = %q, want %q", path[0], src)
		}
		if path[len(path)-1] != dep {
			t.Errorf("path[last] = %q, want %q", path[len(path)-1], dep)
		}
	}

	// Source itself must NOT appear as its own key.
	if _, ok := paths[src]; ok {
		t.Errorf("source %q should not appear in path map", src)
	}
}

// TestTransitiveImportsWithPaths_GhostNodeNotMarked verifies R4-1: a ghost
// reference (PkgPath in another package's Imports but not loaded into the
// graph) must not appear in the path map and must not block re-entry from
// other parents. The previous implementation marked the ghost visited and
// returned, which was harmless for paths but wrong as defensive scaffolding.
func TestTransitiveImportsWithPaths_GhostNodeNotMarked(t *testing.T) {
	t.Parallel()
	const mod = "example.com/ghost"
	src := mod + "/src"
	ghost := mod + "/ghost"

	// src imports a ghost (the ghost package is not in the load).
	srcPkg := &packages.Package{
		PkgPath: src,
		Imports: map[string]*packages.Package{
			ghost: {PkgPath: ghost},
		},
	}
	g := depgraph.FromPackages(mod, []*packages.Package{srcPkg})

	paths := g.TransitiveImportsWithPaths(src)
	if _, ok := paths[ghost]; ok {
		t.Errorf("ghost node %q should not appear in path map; got path %v",
			ghost, paths[ghost])
	}
	if len(paths) != 0 {
		t.Errorf("expected empty path map for ghost-only graph; got %v", paths)
	}
}

// TestTransitiveImports_SelfCycle verifies that a package whose Imports map
// contains its own PkgPath does not cause an infinite loop, and that the
// closure result is empty (the only reachable node is self, which is excluded).
func TestTransitiveImports_SelfCycle(t *testing.T) {
	t.Parallel()
	const mod = "example.com/selfcycle"
	const pkgPath = "example.com/selfcycle/loop"
	selfPkg := &packages.Package{
		PkgPath: pkgPath,
		Imports: map[string]*packages.Package{
			pkgPath: {PkgPath: pkgPath},
		},
	}
	g := depgraph.FromPackages(mod, []*packages.Package{selfPkg})
	got := g.TransitiveImports(pkgPath)
	if len(got) != 0 {
		t.Errorf("TransitiveImports(self-cycle) = %v, want empty", got)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
