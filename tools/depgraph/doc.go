// Package depgraph provides the golang.org/x/tools/go/packages integration
// for building a GoCell dependency graph from live module source.
//
// The core data model (Graph, Node, Stats, layer constants, LayerOf, CellOf,
// SliceOf, TransitiveImports) is defined in kernel/depgraph. Callers that only
// need graph data and layer classification should import kernel/depgraph directly
// to avoid the heavy golang.org/x/tools dependency.
//
// DOT rendering lives here (WriteDOT) rather than in kernel/depgraph, because
// the Graphviz presentation concern belongs in the tools layer, not in kernel.
//
// This package provides the packages.Load entry points:
//
//   - Load(opts LoadOptions, patterns ...string) (*kernel/depgraph.Graph, error) —
//     self-contained, for CLI or one-shot consumers. Auto-detects module
//     path from go.mod and runs packages.Load with the structural modes
//     only (NeedName | NeedImports | NeedModule). Per-package errors
//     surfaced by packages.Load become a fail-fast error so callers never
//     receive a silently partial graph.
//   - FromPackages(module, pkgs) (*kernel/depgraph.Graph) — injection point
//     for callers that already loaded packages (notably archtest, which
//     shares typeseval.SharedResolver across structural and type-level rules).
//     The contract is structural-only: FromPackages reads PkgPath, Imports,
//     and ID from each package and ignores typed fields.
package depgraph
