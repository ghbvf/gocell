package auth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// authorizerKey is an unexported private struct type used as the sole context
// key for the Authorizer funnel. Its unexported type prevents any external
// package from constructing the key and writing an Authorizer into context
// through any path other than WithAuthorizer.
//
// AI-robust Grade: Hard — "sealed construction" + "single sanctioned holder"
// from the ai-robust.md Hard 范本目录.
//
// INVARIANT: AUTHORIZER-CTX-FUNNEL-01
// Upstream: WithAuthorizer is the SOLE injector of an Authorizer into context.
// Downstream: AuthorizerFromContext and RequirePermission are the SOLE readers.
// No other code may write to authorizerKey{} — the unexported type makes any
// out-of-package attempt a compile error.
type authorizerKey struct{}

// WithAuthorizer injects a the PDP Authorizer into the context. It is the sole
// upstream funnel entry point for the Authorizer-in-context path.
//
// Composition roots (bootstrap, cellmodules) call this once when building the
// request context chain (e.g. after AuthMiddleware). Business code — cells,
// handlers, services — must never call this; they consume via
// AuthorizerFromContext or use the RequirePermission Policy which calls it
// internally.
//
// AI-robust Grade: Hard sealed-construction; sole WithAuthorizer upstream +
// sole AuthorizerFromContext/RequirePermission downstream (funnel 双向锁).
func WithAuthorizer(ctx context.Context, a Authorizer) context.Context {
	return context.WithValue(ctx, authorizerKey{}, a)
}

// AuthorizerFromContext retrieves the Authorizer injected by WithAuthorizer.
// Returns (nil, false) when no Authorizer is present — callers should
// treat absence as fail-closed (deny) per the RequirePermission contract.
//
// AI-robust Grade: Hard sealed-construction; sole downstream reader of
// authorizerKey{} together with RequirePermission.
func AuthorizerFromContext(ctx context.Context) (Authorizer, bool) {
	v := ctx.Value(authorizerKey{})
	if v == nil {
		return nil, false
	}
	a, ok := v.(Authorizer)
	if !ok || a == nil {
		return nil, false
	}
	return a, true
}

// msgAuthzPDPNotWired is the canonical const message for the fail-closed guard
// when no Authorizer is in context. A const literal is required by
// MESSAGE-CONST-LITERAL-01.
const msgAuthzPDPNotWired = "authorization policy engine not wired"

// msgInsufficientPermissions is the canonical const message for a PDP deny
// returned by RequirePermission.
const msgInsufficientPermissions = "insufficient permissions"

// RequirePermission returns a Policy that enforces the caller holds the given
// authz.Permission according to the PDP (Authorizer) wired in context.
//
// It is the SOLE PDP route entry downstream of the Authorizer-in-context
// funnel; business routes declare ABAC gates with this function.
//
// Decision logic (fail-closed at every step):
//  1. Principal absent or PrincipalUser with empty Subject → KindUnauthenticated
//     (HTTP 401). Authentication must precede authorization.
//  2. Authorizer absent from context → KindPermissionDenied (HTTP 403).
//     An unwired PDP must never silently permit; this is a misconfiguration
//     guard that catches routes wired before the composition root injects the
//     Authorizer.
//  3. authorizer.Authorize(ctx, subject, r.URL.Path, p.String()) → on error,
//     the errcode is returned verbatim so httputil maps the correct HTTP status
//     (KindUnavailable → 503 when the policy store is down,
//     KindPermissionDenied → 403 when the request lacks a tenant scope).
//  4. Decision.IsAllow() → nil (permit). Else → KindPermissionDenied (403).
//
// RequirePermission intentionally does NOT consume dec.Obligations()
// (RowScope/FieldMask). This is a coarse allow/deny gate; obligation enforcement
// at the data-read PEPs (repo/projection) lands in PR-11/PR-12. Denying on a
// non-zero obligation here would wrongly block a legitimately allowed-with-
// masking request.
//
// AI-robust Grade: Hard — downstream of the sealed authorizerKey funnel;
// sole PDP route entry for permission-based authorization.
func RequirePermission(p authz.Permission) Policy {
	return func(r *http.Request) error {
		principal, ok := FromContext(r.Context())
		if !ok {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgAuthRequired)
		}
		// G1.B: defense-in-depth; mirrors RequireAnyRole / RequireSelfOrRole.
		// PrincipalUser must always carry a non-empty Subject.
		if principal.Kind == PrincipalUser && principal.Subject == "" {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgAuthRequired)
		}

		authorizer, ok := AuthorizerFromContext(r.Context())
		if !ok {
			// Fail-closed: an unwired PDP is a misconfiguration; deny all requests.
			loggerFrom(r.Context()).Error(
				"authz: RequirePermission called with no Authorizer in context — denying (fail-closed)",
				slog.String("path", r.URL.Path),
				slog.String("subject", principal.Subject),
				slog.String("permission", p.String()),
			)
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgAuthzPDPNotWired)
		}

		dec, err := authorizer.Authorize(r.Context(), principal.Subject, r.URL.Path, p.String())
		if err != nil {
			// Transparent passthrough: the errcode Kind drives the HTTP status.
			// KindUnavailable → 503 (policy store down); KindPermissionDenied →
			// 403 (no tenant scope). Log here so the deny reason is observable
			// without requiring callers to re-log.
			loggerFrom(r.Context()).Error(
				"authz: Authorizer.Authorize returned error",
				slog.Any("error", err),
				slog.String("path", r.URL.Path),
				slog.String("subject", principal.Subject),
				slog.String("permission", p.String()),
			)
			return err
		}

		if dec.IsAllow() {
			// RequirePermission does NOT consume dec.Obligations() — see godoc.
			return nil
		}

		loggerFrom(r.Context()).Info(
			"authz: permission denied by PDP",
			slog.String("path", r.URL.Path),
			slog.String("subject", principal.Subject),
			slog.String("permission", p.String()),
			slog.String("reason", dec.Reason()),
		)
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgInsufficientPermissions)
	}
}
