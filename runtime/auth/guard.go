package auth

import (
	"net/http"

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
// If the path value matches the strict canonical UUID form (36-char dashed or
// 32-char compact, any case), it is normalized to lowercase canonical dashed
// form before comparison. Brace-wrapped, urn:uuid:, and whitespace-padded
// shapes are NOT treated as UUIDs here even though google/uuid.Parse accepts
// them — they fall through to verbatim comparison and almost always fail to
// match a canonical subject, leaving the bypassRoles check as the only path
// to authorization. Non-UUID path values (e.g. roleName) are compared
// verbatim.
//
// If the path parameter is empty (route does not carry it), the check falls
// back to role-only; prefer AnyRole for those endpoints.
func SelfOr(pathParam string, bypassRoles ...string) Policy {
	return func(r *http.Request) error {
		raw := r.PathValue(pathParam)
		// PR-A45: align with handler-edge ParseUUIDPathParam — normalize the
		// strict canonical UUID forms so authorization is stable across
		// canonical/uppercase/compact representations.
		if canonical, ok := httputil.ParseCanonicalUUID(raw); ok {
			raw = canonical
		}
		return RequireSelfOrRole(r.Context(), raw, bypassRoles...)
	}
}
