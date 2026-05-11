// Package ctxkeys provides typed context keys for Cell-model identifiers
// (cell, slice, journey, contract) propagated through context.Context.
//
// Generic observability and networking keys (request, trace, span,
// correlation, real IP) live in github.com/ghbvf/gocell/pkg/ctxkeys.
// Cell-model keys belong here because they encode GoCell architectural
// concepts rather than generic cross-service conventions.
package ctxkeys
