package depgraph_test

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/depgraph"
)

const synthModule = "example.com/synth"

// loadSynth loads the testdata/synth fake module.
func loadSynth(t *testing.T, includeTests bool) *depgraph.Graph {
	t.Helper()
	dir, err := filepath.Abs("testdata/synth")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	g, err := depgraph.Load(depgraph.LoadOptions{
		Dir:          dir,
		IncludeTests: includeTests,
	}, "./...")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return g
}

func TestLoad_AutoDetectsModule(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	if g.Module != synthModule {
		t.Errorf("Module = %q, want %q", g.Module, synthModule)
	}
}

func TestLoad_BuildsNodeMap(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)

	// `./...` matches every package directory under the module root, so
	// testhelper appears in the load even with IncludeTests=false. The
	// distinction matters only for TestOnly marking, not for which nodes
	// exist in the graph.
	wantPkgs := []string{
		synthModule + "/a",
		synthModule + "/b",
		synthModule + "/c",
		synthModule + "/cells/cellA",
		synthModule + "/d",
		synthModule + "/generated/foo",
		synthModule + "/testhelper",
	}
	got := make([]string, 0, len(g.Packages))
	for _, n := range g.Packages {
		got = append(got, n.ID)
	}
	sort.Strings(got)
	sort.Strings(wantPkgs)

	if !equalStrings(got, wantPkgs) {
		t.Errorf("Packages IDs:\n got=%v\nwant=%v", got, wantPkgs)
	}
}

func TestLoad_PreservesDirectImports(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)

	a := g.ByID(synthModule + "/a")
	if a == nil {
		t.Fatal("missing pkg a")
	}
	wantImports := []string{synthModule + "/b", synthModule + "/d"}
	got := append([]string(nil), a.Imports...)
	sort.Strings(got)
	sort.Strings(wantImports)
	if !equalStrings(got, wantImports) {
		t.Errorf("a.Imports = %v, want %v", got, wantImports)
	}
}

func TestLoad_LayerAndCellAnnotation(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)

	// Layer assignment is derived from the first path segment under the
	// module root. The synth fixture uses non-GoCell directory names (a,
	// b, c, d, testhelper) so those classify as LayerThirdParty even
	// though they are inside the module — that is the documented
	// fallthrough. cells/ and generated/ are recognized.
	cases := []struct {
		id        string
		wantLayer string
		wantCell  string
	}{
		{synthModule + "/a", depgraph.LayerThirdParty, ""},
		{synthModule + "/cells/cellA", depgraph.LayerCells, "cellA"},
		{synthModule + "/generated/foo", depgraph.LayerGenerated, ""},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			n := g.ByID(c.id)
			if n == nil {
				t.Fatalf("missing node %q", c.id)
			}
			if n.Layer != c.wantLayer {
				t.Errorf("Layer = %q, want %q", n.Layer, c.wantLayer)
			}
			if n.CellID != c.wantCell {
				t.Errorf("CellID = %q, want %q", n.CellID, c.wantCell)
			}
		})
	}
}

func TestLoad_TestOnlyMarking(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, true)

	helper := g.ByID(synthModule + "/testhelper")
	if helper == nil {
		t.Fatal("testhelper not loaded with IncludeTests=true")
	}
	if !helper.TestOnly {
		t.Error("testhelper.TestOnly = false, want true (only imported from a_test.go)")
	}

	a := g.ByID(synthModule + "/a")
	if a == nil {
		t.Fatal("missing pkg a")
	}
	if a.TestOnly {
		t.Error("a.TestOnly = true, want false (production package)")
	}
}

func TestLoad_RawPackagesAccessor(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	raw := g.RawPackages()
	if len(raw) == 0 {
		t.Fatal("RawPackages() empty")
	}
	for _, p := range raw {
		if p.PkgPath == "" {
			t.Errorf("raw package has empty PkgPath: %+v", p)
		}
	}
}

func TestLoad_StatsCountsPackagesAndEdges(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	if g.Stats.Packages != len(g.Packages) {
		t.Errorf("Stats.Packages = %d, want %d", g.Stats.Packages, len(g.Packages))
	}
	wantEdges := 0
	for _, n := range g.Packages {
		wantEdges += len(n.Imports)
	}
	if g.Stats.Edges != wantEdges {
		t.Errorf("Stats.Edges = %d, want %d", g.Stats.Edges, wantEdges)
	}
}

func TestLoad_NoPatternsErr(t *testing.T) {
	t.Parallel()
	dir, err := filepath.Abs("testdata/synth")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if _, err := depgraph.Load(depgraph.LoadOptions{Dir: dir}, "./does-not-exist/..."); err == nil {
		t.Error("Load with non-matching pattern: want error, got nil")
	}
}

func TestFromPackages_ExplicitModuleOverride(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	rebuilt := depgraph.FromPackages("forced.example/different", g.RawPackages())
	if rebuilt.Module != "forced.example/different" {
		t.Errorf("FromPackages module override: got %q, want %q",
			rebuilt.Module, "forced.example/different")
	}
}

func equalStrings(a, b []string) bool {
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
