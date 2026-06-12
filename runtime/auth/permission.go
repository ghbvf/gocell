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

// RequirePermissionOrSelf returns a Policy that permits the request when the
// authenticated subject is accessing its OWN resource — the path parameter named
// by pathParam equals the subject — and otherwise delegates to RequirePermission(p)
// so the PDP decides.
//
// It is the migration target for the legacy auth.SelfOr(pathParam, RoleAdmin)
// ownership gates (accesscore identitymanage/rbaccheck, PR-10c #1348): the
// role-bypass branch becomes a permission gate; the self branch stays a
// request-shape exemption.
//
// Self-exemption is a REQUEST-SHAPE determination (.claude/rules/gocell/tenancy.md
// §ABAC authz): a caller naming itself in the path reads/writes its own resource
// and is exempt from the route PDP; the data layer still governs row visibility
// via RowScope. This is NOT a second PDP path — every non-self request flows
// through the sole RequirePermission gate (the single PDP route entry, ADR §D5).
// Self-exemption is gated on PrincipalUser with a non-empty Subject: service and
// anonymous principals never self-exempt, they fall through to the PDP
// (fail-closed). The self comparison (isSelfAccess) normalizes both the path value
// and the subject to canonical UUID form, mirroring RequireSelfOrRole.
//
// Empty param ≠ self (tenancy.md): an empty path value never matches a non-empty
// subject (isSelfAccess returns false on empty target), so it falls through to the
// PDP — a subject can only self-exempt by explicitly naming itself.
//
// Caller contract (review-enforced, not type-expressible): pathParam MUST name
// the subject-identity path parameter of the route (the target user/owner id) —
// pointing it at an unrelated param would exempt non-owners.
//
// AI-robust Grade: Medium — the self-check is a runtime param==subject judgement
// (not type-expressible); it fails closed (non-user principal, empty subject, or
// empty/mismatched param → delegates to RequirePermission → 401/403). Hard-ification
// rides the PR-13 codegen funnel together with RequirePermission.
func RequirePermissionOrSelf(pathParam string, p authz.Permission) Policy {
	requirePermission := RequirePermission(p)
	return func(r *http.Request) error {
		if principal, ok := FromContext(r.Context()); ok &&
			principal.Kind == PrincipalUser && principal.Subject != "" &&
			isSelfAccess(principal.Subject, r.PathValue(pathParam)) {
			return nil
		}
		return requirePermission(r)
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
