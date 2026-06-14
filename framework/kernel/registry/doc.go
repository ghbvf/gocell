// Package registry provides in-memory registries for Cell and Contract
// instances, used by kernel/assembly to look up dependencies at init time
// and by kernel/governance for cross-reference validation.
//
// Lifecycle: registries are populated once during assembly bootstrap
// (phase2 in runtime/bootstrap) and remain immutable afterward. Lookups
// are concurrent-safe by virtue of immutability, not via locking.
package registry
