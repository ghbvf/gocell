package depgraph_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ghbvf/gocell/kernel/depgraph"
)

// buildSynth constructs a synthetic Graph that exercises the wire-format
// fields without requiring packages.Load (which lives in tools/depgraph).
//
//	a → b → c (module-internal DAG)
//	a → d
//	cells/cellA (leaf, no imports)
//	testhelper (TestOnly — marked by MarkTestOnly)
func buildSynth(withTestOnly bool) *depgraph.Graph {
	const mod = "example.com/synth"
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

func TestGraphMarshalJSON_Deterministic(t *testing.T) {
	t.Parallel()
	g := buildSynth(false)
	first, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal first: %v", err)
	}
	second, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("MarshalJSON not deterministic:\n first=%s\nsecond=%s", first, second)
	}
}

func TestGraphMarshalJSON_StableFieldNames(t *testing.T) {
	t.Parallel()
	g := buildSynth(true)
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Wire-format contract: every field below MUST appear in the output.
	// Renaming any of these is a breaking change requiring a major bump.
	mustContain := []string{
		`"module":`,
		`"packages":[`,
		`"stats":{"packages":`,
		`"id":"`,
		`"layer":"`,
		`"imports":[`,
	}
	body := string(raw)
	for _, want := range mustContain {
		if !containsStr(body, want) {
			t.Errorf("wire format missing required key: %q\nfull=%s", want, body)
		}
	}

	// cellId and testOnly are omitempty — assert they appear at least once.
	for _, want := range []string{`"cellId":"cellA"`, `"testOnly":true`} {
		if !containsStr(body, want) {
			t.Errorf("expected fixture to exercise omitempty key: %q\nbody=%s", want, body)
		}
	}
}

func TestGraphMarshalJSON_DoesNotMutateOriginal(t *testing.T) {
	t.Parallel()
	g := buildSynth(false)

	// Find a node with at least one import to make the test meaningful.
	var target *depgraph.Node
	for _, n := range g.Packages {
		if len(n.Imports) > 0 {
			target = n
			break
		}
	}
	if target == nil {
		t.Skip("no node with imports in synth fixture")
	}

	// Snapshot the Imports slice before marshaling.
	before := append([]string(nil), target.Imports...)

	if _, err := json.Marshal(g); err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// After marshaling the original imports must be unchanged in order.
	after := target.Imports
	if len(after) != len(before) {
		t.Fatalf("MarshalJSON mutated Imports length: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("MarshalJSON mutated Imports[%d]: before=%q after=%q", i, before[i], after[i])
		}
	}
}

func TestGraphMarshalJSON_NilGraph(t *testing.T) {
	t.Parallel()
	var g *depgraph.Graph
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal(nil): %v", err)
	}
	if string(raw) != "null" {
		t.Errorf("nil graph JSON = %q, want null", raw)
	}
}

func TestFromNodes_DedupAndSort(t *testing.T) {
	t.Parallel()
	const mod = "example.com/test"
	nodes := []*depgraph.Node{
		{ID: mod + "/z", Layer: depgraph.LayerUnknown, Imports: []string{}},
		{ID: mod + "/a", Layer: depgraph.LayerUnknown, Imports: []string{mod + "/z"}},
		{ID: mod + "/z", Layer: depgraph.LayerUnknown, Imports: []string{}}, // duplicate
	}
	g := depgraph.FromNodes(mod, nodes)
	if g.Stats.Packages != 2 {
		t.Errorf("Stats.Packages = %d, want 2 (dedup)", g.Stats.Packages)
	}
	if g.Stats.Edges != 1 {
		t.Errorf("Stats.Edges = %d, want 1", g.Stats.Edges)
	}
	if len(g.Packages) < 2 || g.Packages[0].ID != mod+"/a" {
		t.Errorf("Packages not sorted by ID; got %v", packageIDs(g))
	}
}

func TestMarkTestOnly(t *testing.T) {
	t.Parallel()
	const mod = "example.com/test"
	helper := &depgraph.Node{ID: mod + "/helper", Layer: depgraph.LayerUnknown, Imports: []string{}}
	prod := &depgraph.Node{ID: mod + "/prod", Layer: depgraph.LayerUnknown, Imports: []string{}}
	g := depgraph.FromNodes(mod, []*depgraph.Node{helper, prod})

	prodImporters := map[string]bool{mod + "/prod": true}
	testImporters := map[string]bool{mod + "/helper": true}
	depgraph.MarkTestOnly(g, prodImporters, testImporters)

	if n := g.ByID(mod + "/helper"); n == nil || !n.TestOnly {
		t.Errorf("helper.TestOnly = false, want true")
	}
	if n := g.ByID(mod + "/prod"); n == nil || n.TestOnly {
		t.Errorf("prod.TestOnly = true, want false")
	}
}

func packageIDs(g *depgraph.Graph) []string {
	ids := make([]string, len(g.Packages))
	for i, n := range g.Packages {
		ids[i] = n.ID
	}
	return ids
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
