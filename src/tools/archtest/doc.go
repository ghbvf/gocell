// Package archtest enforces Go import layering rules for the GoCell architecture.
//
// Rules enforced (from CLAUDE.md):
//
//   LAYER-01: kernel/ must not import runtime/, adapters/, or cells/
//   LAYER-02: cells/ must not import adapters/
//   LAYER-03: runtime/ must not import cells/ or adapters/
//   LAYER-04: adapters/ must not import cells/, cmd/, or examples/
//   LAYER-05: cells/A must not import cells/B/internal/ (cross-cell isolation)
package archtest
