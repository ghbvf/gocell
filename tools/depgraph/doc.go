// Package depgraph builds a structural package-level dependency graph for
// the GoCell repository. It powers archtest's transitive-closure layer
// rules (LAYER-05T/06T/09T) and the `gocell graph` CLI subcommand. Type-
// level rules (archtest's LAYER-08 / LAYER-10) load packages independently
// via tools/archtest/internal/typeseval; this package is structural-only.
//
// # Wire format stability
//
// The exported types Graph, Node, and Stats define a stable JSON wire
// contract consumed by:
//
//   - tools/archtest (golden file tests guard breakage)
//   - cmd/gocell/app/graph.go (CLI export)
//   - Track J devtools (J1 metadata + J2 HTTP handler, planned)
//
// Stability: stable. Breaking changes (renamed/removed JSON tags, removed
// types) require a major version bump and coordinated update of all
// consumers. New optional fields with `omitempty` are non-breaking.
//
// Node.Layer takes one of:
//
//	kernel, runtime, adapters, cells, pkg, cmd, examples, tools, tests,
//	generated, root, stdlib, thirdparty, unknown
//
// "unknown" marks a package whose import path is module-internal but
// whose first segment is not in the known-bucket list — see LayerOf for
// why this is distinct from "thirdparty". Adding new layer values is a
// non-breaking addition.
//
// # Construction
//
// Two entry points address two audiences:
//
//   - Load(patterns ...string) — self-contained, for CLI or one-shot
//     consumers. Auto-detects module path from go.mod and runs
//     packages.Load with the structural modes only
//     (NeedName | NeedImports | NeedModule). Callers that need typed
//     analysis must use a separate loader.
//   - FromPackages(module, pkgs) — injection point for callers that
//     already loaded packages (notably archtest, which shares
//     typeseval.SharedResolver across structural and type-level rules).
//     The contract is structural-only: FromPackages reads PkgPath,
//     Imports, and ID from each package and ignores typed fields
//     (Types / TypesInfo / Syntax / Errors). pkgs is consumed during
//     construction and not retained on the returned Graph, so callers
//     are free to mutate or discard it after the call returns.
//     TestFromPackages_StructuralOnlyContract locks this boundary.
//
// # Layer inference
//
// LayerOf assigns each package to one of the Node.Layer values listed
// above. The rules live in layer.go and are the single source of truth
// for archtest (which previously duplicated them).
//
// ref: loov/goda internal/pkggraph (https://github.com/loov/goda) — the
// Graph/Node data model is borrowed without taking the dependency. The
// stat.Stat aggregation, Color rendering metadata, and pkgset expression
// language are dropped as unnecessary for layering enforcement.
package depgraph
