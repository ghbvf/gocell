// Package ctxkeys provides typed context keys and helper functions for
// propagating GoCell identifiers (cell, slice, correlation, journey, trace,
// span, request, ip) through context.Context.
//
// Authentication subject is propagated via runtime/auth.Principal —
// use auth.WithPrincipal / auth.FromContext instead of a ctxkeys entry.
package ctxkeys
