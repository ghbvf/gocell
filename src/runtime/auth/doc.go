// Package auth provides authentication and authorization middleware for the
// GoCell HTTP runtime.
//
// It defines interfaces for token validation, session lookup, and RBAC
// enforcement. Concrete implementations are provided by adapters (e.g.
// adapters/oidc for OIDC-based token validation).
//
// # Middleware Chain
//
// The typical middleware chain is:
//
//  1. ExtractToken — extracts Bearer token from Authorization header
//  2. ValidateToken — verifies JWT signature, issuer, audience, and expiry
//  3. EnrichContext — adds cell_id, slice_id, correlation_id to context
//  4. EnforceRBAC — checks role-based access control policies
//
// # Context Keys
//
// Authenticated identity is propagated via pkg/ctxkeys context keys.
package auth
