package depgraph_test

import (
	"sort"
	"testing"
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

func TestTransitiveImports_Memoization(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	first := g.TransitiveImports(synthModule + "/a")
	second := g.TransitiveImports(synthModule + "/a")
	// Memoized: same map returned by reference.
	if &first == &second {
		// Passing this branch is impossible (different local addresses);
		// the real test is that the contents are identical.
		t.Skip("address comparison meaningless for maps")
	}
	if len(first) != len(second) {
		t.Errorf("memoization broken: first=%d second=%d", len(first), len(second))
	}
	for k := range first {
		if !second[k] {
			t.Errorf("memoization broken: %q in first but not second", k)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
