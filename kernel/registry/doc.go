// Package registry provides in-memory registries for Cell and Contract
// instances, used by kernel/assembly to look up dependencies at init time
// and by kernel/governance for cross-reference validation.
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/registry imports only kernel/metadata. The registries are
// indexes built from a parsed ProjectMeta; they carry no runtime cell
// behavior. kernel/assembly and kernel/governance import kernel/registry;
// nothing in the reverse direction.
//
// Lifecycle: registries are populated once during assembly bootstrap
// (phase2 in runtime/bootstrap) and remain immutable afterward. Lookups
// are concurrent-safe by virtue of immutability, not via locking.
package registry
