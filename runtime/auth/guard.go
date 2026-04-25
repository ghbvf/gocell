package auth

import (
	"net/http"
)

// Policy evaluates an HTTP request for authorization. A nil return permits
// the request; a non-nil errcode error short-circuits with the mapped status.
// Taking *http.Request (not just context) lets policies read path/query
// params for self-access checks without handler cooperation.
//
// ref: go-chi/jwtauth Authenticator — middleware wraps handler; error-only
// decision. Deviated: Policy takes *http.Request instead of context so that
// SelfOr can read path values directly (r.PathValue) without the handler
// passing the target ID in explicitly.
// ref: grpc-ecosystem/go-grpc-middleware WithServerUnaryInterceptor — interceptor
// pattern where auth is declared at registration time, not inside handler body.
type Policy func(r *http.Request) error

// AnyRole returns a Policy that requires the subject to hold at least one of
// the given roles. Wraps RequireAnyRole.
//
// Footgun: calling AnyRole() with zero roles produces a Policy that always
// returns ErrAuthForbidden (no role can match). Pass at least one named role,
// or use Public:true on the auth.Route if the endpoint should be unauthenticated.
func AnyRole(roles ...string) Policy {
	return func(r *http.Request) error {
		return RequireAnyRole(r.Context(), roles...)
	}
}

// SelfOr returns a Policy that permits the request when the subject equals the
// path parameter value, or when the subject holds one of bypassRoles.
// pathParam is the name of the path parameter (e.g. "id", "userID") whose
// value is compared against the authenticated subject via r.PathValue.
//
// If the path parameter is empty (route does not carry it), the check falls
// back to role-only; prefer AnyRole for those endpoints.
func SelfOr(pathParam string, bypassRoles ...string) Policy {
	return func(r *http.Request) error {
		return RequireSelfOrRole(r.Context(), r.PathValue(pathParam), bypassRoles...)
	}
}
