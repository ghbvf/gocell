package auth

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
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

// Secured wraps an http.HandlerFunc with a Policy. The returned handler
// evaluates the policy first; on failure it writes the mapped domain error
// and returns without invoking the wrapped handler.
//
// policy must not be nil; passing nil panics immediately at wrap time to make
// misuse detectable during startup/test rather than silently skipping authz.
//
// ref: go-chi/jwtauth jwtauth.go Authenticator — write response inside the
// guard, caller only short-circuits.
// ref: grpc-ecosystem/go-grpc-middleware — interceptor declared at route
// registration, not inline in handler body.
func Secured(h http.HandlerFunc, policy Policy) http.HandlerFunc {
	if policy == nil {
		panic("auth.Secured: policy must not be nil")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if err := policy(r); err != nil {
			httputil.WriteDomainError(r.Context(), w, err)
			return
		}
		h(w, r)
	}
}

// Authenticated returns a Policy that requires an authenticated subject
// (any non-empty subject in context). Use for endpoints that only need
// to verify a user is logged in, regardless of role.
func Authenticated() Policy {
	return func(r *http.Request) error {
		if subject, ok := ctxkeys.SubjectFrom(r.Context()); !ok || subject == "" {
			return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
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
