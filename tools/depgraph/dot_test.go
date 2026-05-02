package depgraph_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/depgraph"
)

func TestWriteDOT_StructuralAssertions(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)

	var sb strings.Builder
	if err := g.WriteDOT(&sb); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}
	out := sb.String()

	if !strings.HasPrefix(out, "digraph depgraph {") {
		t.Errorf("DOT missing header: %s", out[:64])
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "}") {
		t.Errorf("DOT missing closing brace")
	}
	// Lock trailing newline: POSIX text files end with \n, and downstream
	// pipes (e.g. `gocell graph --format=dot | dot -Tsvg`) expect it.
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("DOT output must end with \"}\\n\"; last bytes: %q", out[max(0, len(out)-8):])
	}

	// Every loaded node ID appears exactly once as a node declaration
	// inside its layer cluster.
	for _, n := range g.Packages {
		want := `"` + n.ID + `" [fillcolor=`
		if !strings.Contains(out, want) {
			t.Errorf("DOT missing node declaration for %q", n.ID)
		}
	}

	// Each layer present in the graph is rendered as a cluster label.
	wantLayers := map[string]bool{}
	for _, n := range g.Packages {
		wantLayers[n.Layer] = true
	}
	for layer := range wantLayers {
		want := `label="` + layer + `";`
		if !strings.Contains(out, want) {
			t.Errorf("DOT missing cluster label for layer %q", layer)
		}
	}

	// Every direct edge appears.
	for _, n := range g.Packages {
		for _, dep := range n.Imports {
			edge := `"` + n.ID + `" -> "` + dep + `";`
			if !strings.Contains(out, edge) {
				t.Errorf("DOT missing edge: %s", edge)
			}
		}
	}
}

func TestWriteDOT_Deterministic(t *testing.T) {
	t.Parallel()
	g := loadSynth(t, false)
	var a, b strings.Builder
	if err := g.WriteDOT(&a); err != nil {
		t.Fatalf("WriteDOT first: %v", err)
	}
	if err := g.WriteDOT(&b); err != nil {
		t.Fatalf("WriteDOT second: %v", err)
	}
	if a.String() != b.String() {
		t.Error("WriteDOT not deterministic; two renders of the same graph differ")
	}
}

func TestWriteDOT_NilGraph(t *testing.T) {
	t.Parallel()
	var nilGraph *depgraph.Graph
	var sb strings.Builder
	if err := nilGraph.WriteDOT(&sb); err != nil {
		t.Fatalf("WriteDOT(nil): %v", err)
	}
	if !strings.Contains(sb.String(), "digraph depgraph") {
		t.Errorf("nil-graph DOT missing header: %q", sb.String())
	}
}
