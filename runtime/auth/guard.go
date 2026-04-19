package auth

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
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

// Authenticated returns a Policy that requires an authenticated Principal in
// context. Use for endpoints that only need to verify a user is logged in,
// regardless of role. Returns ErrAuthUnauthorized when no Principal is present
// or when the Principal is a PrincipalUser with an empty Subject (defence-in-depth
// against malformed JWT tokens that slip past the primary authenticator).
func Authenticated() Policy {
	return func(r *http.Request) error {
		p, ok := FromContext(r.Context())
		if !ok {
			return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
		}
		// G1.B: Defence-in-depth. PrincipalUser must always carry a non-empty
		// Subject. PrincipalService is always ServiceNameInternal (non-empty);
		// PrincipalAnonymous Subject is intentionally empty by design.
		if p.Kind == PrincipalUser && p.Subject == "" {
			return errcode.New(errcode.ErrAuthUnauthorized, "principal subject missing")
		}
		return nil
	}
}

// AnyRole returns a Policy that requires the subject to hold at least one of
// the given roles. Wraps RequireAnyRole.
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
