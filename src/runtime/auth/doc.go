// Package auth defines authentication and authorization interfaces for the
// GoCell framework. Concrete implementations live in cells/access-core or
// adapters/oidc; this package contains only interface definitions and context
// helpers.
//
// The package separates authentication (authn) from authorization (authz) via
// two independent interfaces: TokenVerifier and Authorizer. Both are injected
// at bootstrap time, keeping runtime/ decoupled from specific JWT libraries or
// policy engines.
//
// ref: go-kratos/kratos middleware/auth/auth.go — auth middleware pattern
// Adopted: middleware wrapping pattern, Claims extraction from context.
// Deviated: split TokenVerifier and Authorizer (GoCell separates authn from
// authz); no library dependency at this layer.
//
// # Usage
//
//	// Inject verifier and authorizer at bootstrap time:
//	authMW := auth.Middleware(verifier)
//	requireAdmin := auth.RequireRole(authorizer, "admin")
//
//	// Read claims from a handler:
//	claims, ok := auth.ClaimsFrom(r.Context())
//
// # Error Codes
//
// TokenVerifier returning an error produces HTTP 401. Authorizer returning
// false produces HTTP 403. Error codes use pkg/errcode conventions.
package auth
