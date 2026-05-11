// Package metadata provides a parser that loads all GoCell YAML descriptors
// (cell.yaml, slice.yaml, contract.yaml, assembly.yaml, journey.yaml) from
// a project root and produces a validated ProjectMeta model.
//
// metadata is the single source of truth for the metadata schema (see
// KERNEL-METADATA-NO-WIRE-01 archtest, which forbids wire-format symbol
// names from appearing here — those belong to runtime/devtools/catalog).
package metadata
