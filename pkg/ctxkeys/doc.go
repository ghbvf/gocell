// Package ctxkeys provides typed context keys for generic observability and
// networking identifiers (correlation, trace, span, request, real IP)
// propagated through context.Context.
//
// Cell-model identifiers (cell, slice, journey) live in kernel/ctxkeys
// because they encode GoCell architectural concepts rather than generic
// cross-service conventions.
//
// Authentication subject is propagated via runtime/auth.Principal —
// use auth.WithPrincipal / auth.FromContext instead of a ctxkeys entry.
package ctxkeys
