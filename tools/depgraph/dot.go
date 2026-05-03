package depgraph

import (
	"fmt"
	"io"
	"sort"
	"strings"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

// layerColors maps each layer to a Graphviz fill color. Stable colors let
// rendered SVGs be visually compared across versions. New layers default
// to gray; the map is the single source of truth.
var layerColors = map[string]string{
	kerneldepgraph.LayerKernel:     "#fde68a", // amber-200
	kerneldepgraph.LayerRuntime:    "#bbf7d0", // green-200
	kerneldepgraph.LayerAdapters:   "#bfdbfe", // blue-200
	kerneldepgraph.LayerCells:      "#fbcfe8", // pink-200
	kerneldepgraph.LayerPkg:        "#e9d5ff", // purple-200
	kerneldepgraph.LayerCmd:        "#fed7aa", // orange-200
	kerneldepgraph.LayerExamples:   "#fecaca", // red-200
	kerneldepgraph.LayerTools:      "#d1d5db", // gray-300
	kerneldepgraph.LayerTests:      "#c7d2fe", // indigo-200
	kerneldepgraph.LayerGenerated:  "#fef3c7", // yellow-100
	kerneldepgraph.LayerRoot:       "#ffffff", // white
	kerneldepgraph.LayerStdlib:     "#f3f4f6", // gray-100
	kerneldepgraph.LayerThirdParty: "#e5e7eb", // gray-200
}

// WriteDOT renders g as Graphviz DOT into w. Nodes are grouped into clusters
// by Layer; edges are simple directed arrows.
//
// The output is deterministic: nodes within a cluster and clusters themselves
// are sorted by name. Re-rendering the same Graph produces byte-identical DOT.
func WriteDOT(g *kerneldepgraph.Graph, w io.Writer) error {
	if g == nil {
		_, err := io.WriteString(w, "digraph depgraph {}\n")
		return err
	}

	byLayer := map[string][]*kerneldepgraph.Node{}
	for _, n := range g.Packages {
		byLayer[n.Layer] = append(byLayer[n.Layer], n)
	}

	layers := make([]string, 0, len(byLayer))
	for l := range byLayer {
		layers = append(layers, l)
	}
	sort.Strings(layers)

	var b strings.Builder
	b.WriteString("digraph depgraph {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, style=\"filled,rounded\", fontname=\"Helvetica\"];\n")
	fmt.Fprintf(&b, "  label=%q;\n", "module: "+g.Module)

	// Clusters by layer.
	for i, layer := range layers {
		nodes := byLayer[layer]
		sort.Slice(nodes, func(a, bIdx int) bool { return nodes[a].ID < nodes[bIdx].ID })
		color, ok := layerColors[layer]
		if !ok {
			color = "#d1d5db"
		}
		fmt.Fprintf(&b, "  subgraph cluster_%d {\n", i)
		fmt.Fprintf(&b, "    label=%q;\n", layer)
		fmt.Fprintf(&b, "    style=filled;\n")
		fmt.Fprintf(&b, "    color=%q;\n", color)
		fmt.Fprintf(&b, "    fillcolor=%q;\n", "#ffffff")
		for _, n := range nodes {
			fmt.Fprintf(&b, "    %q [fillcolor=%q];\n", n.ID, color)
		}
		b.WriteString("  }\n")
	}

	// Edges.
	for _, n := range g.Packages {
		imports := append([]string(nil), n.Imports...)
		sort.Strings(imports)
		for _, dep := range imports {
			fmt.Fprintf(&b, "  %q -> %q;\n", n.ID, dep)
		}
	}

	b.WriteString("}\n")
	_, err := io.WriteString(w, b.String())
	return err
}
