// Package depgraph defines the core data model for the GoCell package-level
// dependency graph: Graph, Node, Stats, and the layer/cell/slice classifiers.
//
// This package is stdlib-only — it does not depend on golang.org/x/tools or
// any other third-party library. Graph construction from live Go packages is
// handled by tools/depgraph, which imports this package and owns the
// packages.Load integration.
//
// # Types
//
// Graph holds the typed dependency graph for one Go module. Wire format is
// stable JSON; see Graph.MarshalJSON and Node.MarshalJSON.
//
// Node represents one Go package. Edges (Imports) use import-path strings
// (not pointers) so JSON serialization is cycle-safe.
//
// Stats summarizes the graph with package and edge counts.
//
// # Layer classification
//
// LayerOf assigns each import path to a layer string. The canonical layer
// constants (LayerKernel, LayerCells, …) are the JSON values of Node.Layer
// and the single source of truth for archtest layer rules.
//
// CellOf and SliceOf extract the cell/slice ID from a cells/ import path.
//
// # Graph construction
//
// FromNodes builds a Graph from a pre-constructed []*Node slice. The caller
// is responsible for building each Node (ID, Layer, CellID, SliceID, Imports);
// FromNodes handles sorting, byID indexing, Stats, and MarkTestOnly.
//
// MarkTestOnly marks each Node TestOnly=true when it is imported by at least
// one test consumer but no production consumer. Both the production and test
// importer sets are passed as plain string-keyed maps so this package never
// touches golang.org/x/tools types.
//
// # Transitive closure
//
// Graph.TransitiveImports and Graph.TransitiveImportsWithPaths walk
// module-internal production edges. Used by archtest LAYER-05T/06T/09T rules.
package depgraph
