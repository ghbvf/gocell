package depgraph_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/depgraph"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden/*.json fixtures from current output")

func TestGraphMarshalJSON_Deterministic(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
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
	g := loadSynth(t, true)
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
		if !contains(body, want) {
			t.Errorf("wire format missing required key: %q\nfull=%s", want, body)
		}
	}

	// cellId, sliceId, testOnly are omitempty — assert they appear at
	// least once given our fixture has cellA + a testOnly testhelper.
	for _, want := range []string{`"cellId":"cellA"`, `"testOnly":true`} {
		if !contains(body, want) {
			t.Errorf("expected fixture to exercise omitempty key: %q\nbody=%s", want, body)
		}
	}
}

func TestGraphMarshalJSON_GoldenFile(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, true)

	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	raw = append(raw, '\n')

	goldenPath := filepath.Join("testdata", "golden", "graph.json")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(goldenPath, raw, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Logf("updated golden: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("ReadFile golden: %v (run with -update-golden to create)", err)
	}
	if !bytes.Equal(raw, want) {
		t.Errorf("golden mismatch — wire format may have changed.\n"+
			"Run `go test ./tools/depgraph -run GoldenFile -update-golden` to refresh after intentional changes.\n"+
			"Diff (got vs want):\nGOT:\n%s\nWANT:\n%s", raw, want)
	}
}

func TestGraphMarshalJSON_DoesNotMutateOriginal(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)

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

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
