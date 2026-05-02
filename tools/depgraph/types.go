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
type Stats struct {
	Packages int `json:"packages"`
	Edges    int `json:"edges"`
}

// serializedNode is the private serialization view of Node. It mirrors Node's
// wire-format fields so MarshalJSON can sort Imports without mutating the
// live graph's Node values. CellID and SliceID use omitempty like Node.
type serializedNode struct {
	ID       string   `json:"id"`
	Layer    string   `json:"layer"`
	CellID   string   `json:"cellId,omitempty"`
	SliceID  string   `json:"sliceId,omitempty"`
	Imports  []string `json:"imports"`
	TestOnly bool     `json:"testOnly,omitempty"`
}

// MarshalJSON ensures deterministic output: Packages sorted by ID, each
// node's Imports sorted lexicographically. Two graphs with identical
// content produce byte-identical JSON.
//
// Serialization builds a shallow copy of each Node's Imports slice so the
// sort does not mutate the live graph. The original Node.Imports order is
// preserved after the call returns.
func (g *Graph) MarshalJSON() ([]byte, error) {
	if g == nil {
		return []byte("null"), nil
	}
	pkgs := make([]*Node, len(g.Packages))
	copy(pkgs, g.Packages)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })

	serialized := make([]serializedNode, len(pkgs))
	for i, n := range pkgs {
		// Always allocate a non-nil slice so JSON encodes as [] not null,
		// matching the "Imports is never null" contract documented on Node.Imports.
		imports := make([]string, len(n.Imports))
		copy(imports, n.Imports)
		sort.Strings(imports)
		serialized[i] = serializedNode{
			ID:       n.ID,
			Layer:    n.Layer,
			CellID:   n.CellID,
			SliceID:  n.SliceID,
			Imports:  imports,
			TestOnly: n.TestOnly,
		}
	}

	type alias struct {
		Module   string           `json:"module"`
		Packages []serializedNode `json:"packages"`
		Stats    Stats            `json:"stats"`
	}
	return json.Marshal(alias{
		Module:   g.Module,
		Packages: serialized,
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
