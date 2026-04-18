package auth

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Policy is a handler-level authorization predicate evaluated against the
// request context. A nil return means the request may proceed; a non-nil
// error (typically errcode.ErrAuthUnauthorized or ErrAuthForbidden) short-
// circuits with a 401/403 response.
//
// ref: grpc-ecosystem/go-grpc-middleware interceptors/auth/auth.go@main
// (AuthFunc = func(ctx) (context.Context, error) — error-only decision).
// Deviated: we do not return a derived ctx because runtime/auth.AuthMiddleware
// already attaches Claims upstream; Guard only authorizes.
type Policy func(ctx context.Context) error

// Guard evaluates policy against r.Context. On failure it writes the mapped
// HTTP error via httputil.WriteDomainError and returns false — the caller
// should return immediately. On success it returns true.
//
// ref: go-chi/jwtauth jwtauth.go Authenticator — write response inside the
// guard, caller only short-circuits.
func Guard(w http.ResponseWriter, r *http.Request, policy Policy) bool {
	if err := policy(r.Context()); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return false
	}
	return true
}

// AnyRole builds a Policy that requires the subject to hold at least one of
// the given roles. Wraps RequireAnyRole.
func AnyRole(roles ...string) Policy {
	return func(ctx context.Context) error {
		return RequireAnyRole(ctx, roles...)
	}
}

// SelfOr builds a Policy that permits the request when the subject equals
// targetID, or when the subject holds one of the bypassRoles. Wraps
// RequireSelfOrRole.
func SelfOr(targetID string, bypassRoles ...string) Policy {
	return func(ctx context.Context) error {
		return RequireSelfOrRole(ctx, targetID, bypassRoles...)
	}
}
