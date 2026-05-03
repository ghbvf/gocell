package depgraph

import (
	"encoding/json"
	"sort"
)

// Graph is the typed dependency graph for one Go module. Wire-format stable.
type Graph struct {
	Module   string  `json:"module"`
	Packages []*Node `json:"packages"`
	Stats    Stats   `json:"stats"`

	byID map[string]*Node // O(1) lookup; not serialized
}

// Node represents one Go package. Edges (Imports) use ID strings (not pointers)
// so JSON serialization is cycle-safe and round-trippable.
type Node struct {
	ID      string `json:"id"`
	Layer   string `json:"layer"`
	CellID  string `json:"cellId,omitempty"`
	SliceID string `json:"sliceId,omitempty"`
	// Imports is always a non-nil slice in JSON output (leaves emit [], never null).
	Imports  []string `json:"imports"`
	TestOnly bool     `json:"testOnly,omitempty"`
}

// Stats summarizes the graph. Wire-format stable; new counters with
// omitempty are non-breaking additions.
//
// Edges counts every entry in every Node.Imports slice — that includes
// stdlib and third-party targets, NOT just module-internal edges. This
// is intentionally the "raw structural fan-out" number useful for graph
// rendering and CLI reporting, and is distinct from the closure walked
// by Graph.TransitiveImports (which stops at the module boundary).
type Stats struct {
	Packages int `json:"packages"`
	Edges    int `json:"edges"`
}

// MarshalJSON serializes a Node with a deterministic, non-nil Imports
// slice. The receiver value is left untouched (Imports is copied before
// sorting) so the live graph remains in its original construction order.
//
// Defined on *Node — not just Graph.MarshalJSON — so any external consumer
// that marshals a single node (e.g. a Track J HTTP handler returning one
// package's metadata) still gets the stable wire form. Avoids the maintenance
// hazard of a parallel serializedNode struct that has to be kept in sync.
func (n *Node) MarshalJSON() ([]byte, error) {
	if n == nil {
		return []byte("null"), nil
	}
	// nodeAlias strips the MarshalJSON method to avoid infinite recursion
	// when json.Marshal sees the alias value below.
	type nodeAlias Node
	cp := nodeAlias(*n)
	cp.Imports = append([]string(nil), n.Imports...)
	sort.Strings(cp.Imports)
	if cp.Imports == nil {
		// Honor the "Imports is never null" wire contract on leaf nodes
		// constructed without going through FromPackages (e.g. tests).
		cp.Imports = []string{}
	}
	return json.Marshal(cp)
}

// MarshalJSON ensures deterministic output: Packages sorted by ID. Each
// Node's MarshalJSON method handles intra-node sorting and Imports
// non-nil enforcement, so adding a wire field requires editing only Node.
//
// Two graphs with identical content produce byte-identical JSON.
func (g *Graph) MarshalJSON() ([]byte, error) {
	if g == nil {
		return []byte("null"), nil
	}
	pkgs := make([]*Node, len(g.Packages))
	copy(pkgs, g.Packages)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })

	type graphAlias struct {
		Module   string  `json:"module"`
		Packages []*Node `json:"packages"`
		Stats    Stats   `json:"stats"`
	}
	return json.Marshal(graphAlias{
		Module:   g.Module,
		Packages: pkgs,
		Stats:    g.Stats,
	})
}

// ByID returns the node for an import path, or nil if not in the graph.
func (g *Graph) ByID(id string) *Node {
	if g == nil {
		return nil
	}
	return g.byID[id]
}
