// Package ctxkeys provides typed context keys for Cell-model identifiers
// (cell, slice, journey, contract) propagated through context.Context.
//
// Generic observability and networking keys (request, trace, span,
// correlation, real IP) live in github.com/ghbvf/gocell/pkg/ctxkeys.
// Cell-model keys belong here because they encode GoCell architectural
// concepts rather than generic cross-service conventions.
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/ctxkeys is a leaf — it has zero kernel→kernel dependencies.
// kernel/wrapper, kernel/cell, runtime/* and pkg/ctxkeys all consume it
// without re-export. ctxkeys carries no business logic; the value types
// are unexported keys + Set/Get accessors.
package ctxkeys
