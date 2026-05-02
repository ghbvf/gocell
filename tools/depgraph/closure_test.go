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

// TestTransitiveImports_Idempotent renamed from the previous "Memoization"
// test. The cache is verified by mutating the returned map: the second call
// must return the same underlying map (cache hit), so the deletion is visible.
func TestTransitiveImports_Idempotent(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	first := g.TransitiveImports(synthModule + "/a")
	if len(first) == 0 {
		t.Fatal("expected non-empty closure for /a")
	}

	// Delete one key from the returned map. Because TransitiveImports memoizes
	// by returning the cached map directly, the second call must see the same
	// (now mutated) map — proving the cache is reused, not rebuilt.
	var deleted string
	for k := range first {
		deleted = k
		delete(first, k)
		break
	}
	second := g.TransitiveImports(synthModule + "/a")
	if second[deleted] {
		t.Errorf("memoization: deleted key %q still present in second call; cache was not reused", deleted)
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
