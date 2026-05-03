package depgraph_test

import (
	"sort"
	"testing"

	"github.com/ghbvf/gocell/kernel/depgraph"
)

// These tests exercise TransitiveImports and TransitiveImportsWithPaths using
// graphs built via depgraph.FromNodes. No golang.org/x/tools dependency is
// required — kernel/depgraph must stay stdlib-only.
//
// The synth topology mirrors the testdata/synth fixture used by tools/depgraph:
//
//	a → b → c
//	a → d
//	cells/cellA (leaf)
//	testhelper (TestOnly)

const synthModClosure = "example.com/synth"

// buildSynthForClosure constructs the synth graph topology using FromNodes.
func buildSynthForClosure(withTestOnly bool) *depgraph.Graph {
	mod := synthModClosure
	nodes := []*depgraph.Node{
		{ID: mod + "/a", Layer: depgraph.LayerUnknown, Imports: []string{mod + "/b", mod + "/d"}},
		{ID: mod + "/b", Layer: depgraph.LayerUnknown, Imports: []string{mod + "/c"}},
		{ID: mod + "/c", Layer: depgraph.LayerUnknown, Imports: []string{}},
		{ID: mod + "/d", Layer: depgraph.LayerUnknown, Imports: []string{}},
		{ID: mod + "/cells/cellA", Layer: depgraph.LayerCells, CellID: "cellA", Imports: []string{}},
		{ID: mod + "/testhelper", Layer: depgraph.LayerUnknown, Imports: []string{}},
	}
	g := depgraph.FromNodes(mod, nodes)
	if withTestOnly {
		prod := map[string]bool{
			mod + "/a": true, mod + "/b": true, mod + "/c": true, mod + "/d": true,
			mod + "/cells/cellA": true,
		}
		test := map[string]bool{mod + "/testhelper": true}
		depgraph.MarkTestOnly(g, prod, test)
	}
	return g
}

func TestTransitiveImports_DAG(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(false)

	// a → b → c, a → d. closure of a = {b, c, d}.
	got := g.TransitiveImports(synthModClosure + "/a")
	want := []string{
		synthModClosure + "/b",
		synthModClosure + "/c",
		synthModClosure + "/d",
	}
	gotSlice := mapKeys(got)
	sort.Strings(gotSlice)
	sort.Strings(want)
	if !equalStringSlices(gotSlice, want) {
		t.Errorf("TransitiveImports(a) = %v, want %v", gotSlice, want)
	}
}

func TestTransitiveImports_Leaf(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(false)
	got := g.TransitiveImports(synthModClosure + "/c")
	if len(got) != 0 {
		t.Errorf("TransitiveImports(c) = %v, want empty (leaf)", got)
	}
}

func TestTransitiveImports_MissingNode(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(false)
	got := g.TransitiveImports("does.not/exist")
	if len(got) != 0 {
		t.Errorf("TransitiveImports(missing) = %v, want empty", got)
	}
}

func TestTransitiveImports_NilGraph(t *testing.T) {
	t.Parallel()
	var g *depgraph.Graph
	got := g.TransitiveImports("example.com/synth/a")
	if len(got) != 0 {
		t.Errorf("TransitiveImports(nil graph) = %v, want empty", got)
	}
}

func TestTransitiveImports_StaysInModule(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(true) // withTestOnly=true exercises full graph
	for _, id := range []string{
		synthModClosure + "/a",
		synthModClosure + "/b",
		synthModClosure + "/c",
	} {
		closure := g.TransitiveImports(id)
		for dep := range closure {
			if dep != synthModClosure && !hasPrefix(dep, synthModClosure+"/") {
				t.Errorf("closure(%s) includes %s; should stay inside module %s",
					id, dep, synthModClosure)
			}
		}
	}
	// Spot-check: stdlib must never appear in any closure.
	for _, n := range g.Packages {
		closure := g.TransitiveImports(n.ID)
		for dep := range closure {
			if dep == "testing" || dep == "fmt" || dep == "encoding/json" {
				t.Errorf("closure(%s) leaked stdlib package %s", n.ID, dep)
			}
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestTransitiveImports_ExcludesTestOnly(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(true)
	closure := g.TransitiveImports(synthModClosure + "/a")
	if closure[synthModClosure+"/testhelper"] {
		t.Errorf("closure(a) includes testhelper; testOnly nodes should be excluded")
	}
}

// TestTransitiveImports_FreshCopy proves each call returns an independent map.
func TestTransitiveImports_FreshCopy(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(false)
	first := g.TransitiveImports(synthModClosure + "/a")
	if len(first) == 0 {
		t.Fatal("expected non-empty closure for /a")
	}

	snapshot := mapKeys(first)
	sort.Strings(snapshot)

	for k := range first {
		delete(first, k)
	}
	first["bogus"] = true

	second := g.TransitiveImports(synthModClosure + "/a")
	gotSlice := mapKeys(second)
	sort.Strings(gotSlice)
	if !equalStringSlices(gotSlice, snapshot) {
		t.Errorf("second call returned %v, want %v (mutation of first leaked)", gotSlice, snapshot)
	}
	if second["bogus"] {
		t.Errorf("second call contains injected key; results not independent")
	}
}

// TestTransitiveImportsWithPaths_DAG locks the path-recording contract.
func TestTransitiveImportsWithPaths_DAG(t *testing.T) {
	t.Parallel()
	g := buildSynthForClosure(false)
	src := synthModClosure + "/a"
	paths := g.TransitiveImportsWithPaths(src)

	for _, dep := range []string{
		synthModClosure + "/b",
		synthModClosure + "/c",
		synthModClosure + "/d",
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

	if _, ok := paths[src]; ok {
		t.Errorf("source %q should not appear in path map", src)
	}
}

// TestTransitiveImportsWithPaths_GhostNodeNotMarked verifies R4-1: a ghost
// reference must not appear in the path map. Uses FromNodes with a node that
// references a package not in the graph.
func TestTransitiveImportsWithPaths_GhostNodeNotMarked(t *testing.T) {
	t.Parallel()
	const mod = "example.com/ghost"
	src := mod + "/src"
	ghost := mod + "/ghost"

	// src imports a ghost (the ghost package is not in the load).
	srcNode := &depgraph.Node{
		ID:      src,
		Layer:   depgraph.LayerUnknown,
		Imports: []string{ghost},
	}
	g := depgraph.FromNodes(mod, []*depgraph.Node{srcNode})

	paths := g.TransitiveImportsWithPaths(src)
	if _, ok := paths[ghost]; ok {
		t.Errorf("ghost node %q should not appear in path map; got path %v",
			ghost, paths[ghost])
	}
	if len(paths) != 0 {
		t.Errorf("expected empty path map for ghost-only graph; got %v", paths)
	}
}

// TestTransitiveImports_SelfCycle verifies no infinite loop on self-import.
func TestTransitiveImports_SelfCycle(t *testing.T) {
	t.Parallel()
	const mod = "example.com/selfcycle"
	const pkgPath = "example.com/selfcycle/loop"
	loopNode := &depgraph.Node{
		ID:      pkgPath,
		Layer:   depgraph.LayerUnknown,
		Imports: []string{pkgPath}, // self-import
	}
	g := depgraph.FromNodes(mod, []*depgraph.Node{loopNode})
	got := g.TransitiveImports(pkgPath)
	if len(got) != 0 {
		t.Errorf("TransitiveImports(self-cycle) = %v, want empty", got)
	}
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
