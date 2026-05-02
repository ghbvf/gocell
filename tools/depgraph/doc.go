// Package depgraph builds a typed package-level dependency graph for the
// GoCell repository. It powers archtest's transitive-closure layer rules
// (LAYER-05T/06T/09T), the type-level LAYER-08 legacy-interface seal, and
// the `gocell graph` CLI subcommand.
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
// # Construction
//
// Two entry points address two audiences:
//
//   - Load(patterns ...string) — self-contained, for CLI or one-shot
//     consumers. Auto-detects module path from go.mod and runs
//     packages.Load with the modes needed for both import and type
//     analysis.
//   - FromPackages(module, pkgs) — injection point for callers that
//     already loaded packages (notably archtest, which shares
//     typeseval.SharedResolver to avoid duplicate loads).
//
// # Layer inference
//
// LayerOf assigns each package to one of: kernel, runtime, adapters, cells,
// pkg, cmd, examples, tools, generated, root, stdlib, thirdparty. The
// rules live in layer.go and are the single source of truth for archtest
// (which previously duplicated them).
//
// # LAYER-08 type-level scope
//
// archtest's legacy-interface seal is type-level only: declarations via
// the Go type system (named types, function receivers, var types) are
// caught; bare string literals such as struct tags or comment text are
// not. This is intentional — type-level analysis is precise where string
// scanning over-matches into comments and rejects renamed identifiers.
//
// ref: loov/goda internal/pkggraph (https://github.com/loov/goda) — the
// Graph/Node data model is borrowed without taking the dependency. The
// stat.Stat aggregation, Color rendering metadata, and pkgset expression
// language are dropped as unnecessary for layering enforcement.
package depgraph
