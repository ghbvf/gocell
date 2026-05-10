// Package metadata provides a parser that loads all GoCell YAML descriptors
// (cell.yaml, slice.yaml, contract.yaml, assembly.yaml, journey.yaml) from
// a project root and produces a validated ProjectMeta model.
//
// metadata is the single source of truth for the metadata schema (see
// KERNEL-METADATA-NO-WIRE-01 archtest, which forbids wire-format symbol
// names from appearing here — those belong to runtime/devtools/catalog).
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/metadata imports only kernel/cellvocab (for the canonical
// consistency-level rank ordering used by assembly_derive.go before any
// typed cellvocab.Level value is bound). It does NOT import kernel/cell —
// after the G-04 refactor the levelrank sub-package was absorbed into
// cellvocab, removing the only metadata→cell reverse edge.
//
// metadata is consumed by `gocell validate`, `gocell generate`,
// kernel/governance, kernel/registry, and kernel/journey. Nothing in
// kernel/ imports back into kernel/metadata at any layer above this one.
package metadata
