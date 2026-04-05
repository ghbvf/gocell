// Package contract provides runtime support for cross-cell contracts in the
// GoCell kernel.
//
// Contracts define communication boundaries between cells. This package
// handles contract registration, lifecycle management (draft/active/deprecated),
// and runtime validation of contract usage by slices.
//
// Contract kinds: http, event, command, projection.
// See kernel/cell.ContractKind for the full enumeration.
package contract
