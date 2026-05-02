package depgraph

import (
	"encoding/json"
	"sort"

	"golang.org/x/tools/go/packages"
)

// Graph is the typed dependency graph for one Go module. Wire-format stable.
type Graph struct {
	Module   string  `json:"module"`
	Packages []*Node `json:"packages"`
	Stats    Stats   `json:"stats"`

	byID    map[string]*Node           // O(1) lookup; not serialized
	rawPkgs []*packages.Package        // accessor for typeseval reuse
	closure map[string]map[string]bool // memoized transitive closure (production paths)
}

// Node represents one Go package. Edges (Imports) use ID strings (not pointers)
// so JSON serialization is cycle-safe and round-trippable.
type Node struct {
	ID       string   `json:"id"`
	Layer    string   `json:"layer"`
	CellID   string   `json:"cellId,omitempty"`
	SliceID  string   `json:"sliceId,omitempty"`
	Imports  []string `json:"imports"`
	TestOnly bool     `json:"testOnly,omitempty"`
}

// Stats summarizes the graph. Wire-format stable; new counters with
// omitempty are non-breaking additions.
type Stats struct {
	Packages int `json:"packages"`
	Edges    int `json:"edges"`
}

// MarshalJSON ensures deterministic output: Packages sorted by ID, each
// node's Imports sorted lexicographically. Two graphs with identical
// content produce byte-identical JSON.
func (g *Graph) MarshalJSON() ([]byte, error) {
	if g == nil {
		return []byte("null"), nil
	}
	pkgs := make([]*Node, len(g.Packages))
	copy(pkgs, g.Packages)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })
	for _, n := range pkgs {
		sort.Strings(n.Imports)
	}
	type alias struct {
		Module   string  `json:"module"`
		Packages []*Node `json:"packages"`
		Stats    Stats   `json:"stats"`
	}
	return json.Marshal(alias{
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

// RawPackages exposes the underlying *packages.Package slice so callers
// (notably archtest's typeseval-backed LAYER-10) can reuse the same load
// without a second packages.Load. Returns nil if the graph was built
// without retaining raw packages.
func (g *Graph) RawPackages() []*packages.Package {
	if g == nil {
		return nil
	}
	return g.rawPkgs
}
