package depgraph

import "sort"

// FromNodes builds a Graph from a pre-constructed []*Node slice.
// module must be the bare module path (e.g. "github.com/ghbvf/gocell").
// FromNodes handles:
//   - deduplication by Node.ID (first occurrence wins)
//   - byID index construction for O(1) ByID lookups and closure walks
//   - deterministic sort of Packages by ID
//   - Stats computation (package count + edge count)
//
// Callers are responsible for building each Node before passing it in;
// the Layer, CellID, SliceID, and Imports fields are taken as-is.
// MarkTestOnly must be called separately when test-importer information
// is available (see tools/depgraph.FromPackages).
func FromNodes(module string, nodes []*Node) *Graph {
	g := &Graph{
		Module: module,
		byID:   make(map[string]*Node, len(nodes)),
	}
	for _, n := range nodes {
		if n == nil || n.ID == "" {
			continue
		}
		if _, dup := g.byID[n.ID]; dup {
			continue
		}
		g.Packages = append(g.Packages, n)
		g.byID[n.ID] = n
	}
	sort.Slice(g.Packages, func(i, j int) bool { return g.Packages[i].ID < g.Packages[j].ID })
	g.Stats.Packages = len(g.Packages)
	edges := 0
	for _, n := range g.Packages {
		edges += len(n.Imports)
	}
	g.Stats.Edges = edges
	return g
}

// FilterByLayer returns a new Graph containing only the packages whose Layer
// is in allowedLayers. Stats are recomputed from the retained packages.
// The original Graph is not modified. Returns an empty Graph if no packages
// match or if g is nil.
func (g *Graph) FilterByLayer(allowedLayers map[string]bool) *Graph {
	if g == nil {
		return &Graph{Module: "", Packages: []*Node{}, Stats: Stats{}}
	}
	var filtered []*Node
	for _, n := range g.Packages {
		if allowedLayers[n.Layer] {
			filtered = append(filtered, n)
		}
	}
	return FromNodes(g.Module, filtered)
}

// MarkTestOnly tags each node in g TestOnly=true when it is imported by at
// least one test consumer (testImporters) and no production consumer
// (prodImporters). Leaf / orphaned packages (absent from both sets) remain
// TestOnly=false — they may be entry points or unused production code.
//
// Both maps are keyed by import path (the importee). They are produced by
// tools/depgraph.collectImporters, which reads golang.org/x/tools/go/packages
// data — a type this package never touches. Receiving plain map[string]bool
// keeps kernel/depgraph dependency-free from x/tools.
func MarkTestOnly(g *Graph, prodImporters, testImporters map[string]bool) {
	for _, n := range g.Packages {
		if !prodImporters[n.ID] && testImporters[n.ID] {
			n.TestOnly = true
		}
	}
}
