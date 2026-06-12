package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
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

// WithAuthorizer injects the PDP Authorizer into the context. It is the sole
// upstream funnel entry point for the Authorizer-in-context path.
//
// Composition roots (bootstrap, cellmodules) call this once when building the
// request context chain (e.g. after AuthMiddleware). Business code — cells,
// handlers, services — must never call this; they consume via
// AuthorizerFromContext, or via RequirePermission which reads it from context.
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

// msgPermissionNotSpecified is the canonical const message for the fail-closed
// guard when RequirePermission receives a zero authz.Permission{}.
const msgPermissionNotSpecified = "authorization permission not specified"

// msgObligationsNotEnforceable is the canonical const message for the fail-closed
// guard when an Allow decision carries obligations this coarse route gate cannot
// discharge (F5).
const msgObligationsNotEnforceable = "authorization decision carries obligations not enforceable at this gate"

// logAuthorizerError logs an Authorizer.Authorize error at the appropriate level.
// KindPermissionDenied (expected tenant-missing deny) → Warn; all others → Error.
// The error is redacted before logging per observability.md §Redaction.
func logAuthorizerError(l *slog.Logger, err error, path, subject, permission string) {
	var ec *errcode.Error
	kind := errcode.KindInternal
	if errors.As(err, &ec) {
		kind = ec.Kind
	}
	args := []any{
		slog.Any("error", redaction.RedactError(err)),
		slog.Int("kind_status", kind.Status()),
		slog.String("path", path),
		slog.String("subject", subject),
		slog.String("permission", permission),
	}
	if kind == errcode.KindPermissionDenied {
		l.Warn("authz: Authorizer.Authorize returned error", args...)
	} else {
		l.Error("authz: Authorizer.Authorize returned error", args...)
	}
}

// RequirePermission returns a Policy that enforces the caller holds the given
// authz.Permission according to the PDP (Authorizer) wired in context.
//
// It is the SOLE PDP route entry downstream of the Authorizer-in-context
// funnel; business routes declare ABAC gates with this function.
//
// Decision logic (fail-closed at every step):
//  1. Zero Permission → KindPermissionDenied (403). A zero authz.Permission{}
//     is a programmer error; it must never reach the PDP.
//  2. Principal absent or PrincipalUser with empty Subject → KindUnauthenticated
//     (HTTP 401). Authentication must precede authorization.
//  3. Authorizer absent from context → KindPermissionDenied (HTTP 403).
//     An unwired PDP must never silently permit; this is a misconfiguration
//     guard that catches routes wired before the composition root injects the
//     Authorizer.
//  4. authorizer.Authorize(ctx, subject, r.URL.Path, p.String()) → on error,
//     the errcode is returned verbatim so httputil maps the correct HTTP status
//     (KindUnavailable → 503 when the policy store is down,
//     KindPermissionDenied → 403 when the request lacks a tenant scope).
//  5. Decision.IsAllow() → nil (permit). Else → KindPermissionDenied (403).
//
// Obligation handling (F5, amends PR-10a ADR D3): RequirePermission is a coarse
// allow/deny gate — it does NOT discharge obligations (RowScope/FieldMask); that
// is the job of the data-read PEPs (repo/projection) in PR-11/PR-12. But it does
// NOT silently drop them either: an Allow whose dec.Obligations() is non-zero
// requires a PEP this gate cannot run, and dropping a *restricting* obligation
// would let the caller see more than the policy intended. So a non-zero
// obligation on an Allow is fail-closed (deny) until the data PEP lands. The
// built-in baseline carries zero obligations, so the normal path is unaffected;
// only a tenant policy that attaches an obligation to an allow trips this — and
// such a policy is not safely enforceable here yet.
//
// AI-robust Grade: Hard — downstream of the sealed authorizerKey funnel;
// sole PDP route entry for permission-based authorization.
func RequirePermission(p authz.Permission) Policy {
	return func(r *http.Request) error {
		// Zero Permission is a programmer error; fail-closed before any I/O.
		if p.IsZero() {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgPermissionNotSpecified)
		}

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
			logAuthorizerError(loggerFrom(r.Context()), err, r.URL.Path, principal.Subject, p.String())
			return err
		}

		return evaluatePermissionDecision(r.Context(), dec, r.URL.Path, principal.Subject, p.String())
	}
}

// evaluatePermissionDecision maps a PDP Decision to the route-gate outcome:
//   - Allow with zero obligations → nil (permit).
//   - Allow with a non-zero obligation this coarse gate cannot discharge → deny
//     (F5 fail-closed; baseline allows carry zero obligations, so the normal path
//     is unaffected — a tenant allow attaching a RowScope/FieldMask obligation is
//     denied rather than silently dropped, which would widen what the caller sees).
//   - Deny → 403.
//
// Extracted from RequirePermission to keep its cognitive complexity within budget.
func evaluatePermissionDecision(ctx context.Context, dec authz.Decision, path, subject, permission string) error {
	if dec.IsAllow() {
		if obl := dec.Obligations(); !obl.IsZero() {
			loggerFrom(ctx).Warn(
				"authz: Allow carries obligations not enforceable at route gate — denying (fail-closed)",
				slog.String("path", path),
				slog.String("subject", subject),
				slog.String("permission", permission),
			)
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgObligationsNotEnforceable)
		}
		return nil
	}

	loggerFrom(ctx).Info(
		"authz: permission denied by PDP",
		slog.String("path", path),
		slog.String("subject", subject),
		slog.String("permission", permission),
		slog.String("reason", dec.Reason()),
	)
	return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, msgInsufficientPermissions)
}
