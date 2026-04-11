// Package archtest enforces Go source-level import layering rules for the GoCell architecture.
//
// This complements kernel/governance which validates metadata-level dependencies
// (DEP-01 to DEP-03: cell ownership, cycle detection, L0 co-location) from YAML files.
// archtest operates on the Go import graph via `go list -json -e` to catch violations
// that metadata validation cannot see.
//
// Rules enforced (from CLAUDE.md):
//
//   LAYER-01: kernel/ may only import stdlib, pkg/, and kernel/ (allow-list)
//   LAYER-02: cells/ must not import adapters/
//   LAYER-03: runtime/ must not import cells/ or adapters/
//   LAYER-04: adapters/ must not import cells/, cmd/, or examples/
//   LAYER-05: cells/A must not import cells/B/internal/ (cross-cell isolation)
package archtest
