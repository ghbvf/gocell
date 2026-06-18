package interceptor

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// authMetadataKey is the lowercase gRPC metadata key carrying the bearer token,
// per the go-grpc-middleware / RFC 7235 convention. gRPC metadata keys are
// always lowercase, so the HTTP "Authorization" header getter cannot be reused.
const authMetadataKey = "authorization"

// Canonical gRPC status messages for the #2008 PDP gate. Extracted as consts so
// the gate's wire descriptions are single-source within the package (mirroring the
// runtime/auth msg* consts on the HTTP side) and cannot drift across the unary +
// stream paths that share the authorize core.
const (
	msgGRPCNoPermissionMapping       = "no authorization permission mapped for this method"
	msgGRPCAuthRequired              = "authentication required"
	msgGRPCAuthzNotWired             = "authorization policy engine not wired"
	msgGRPCInsufficientPermissions   = "insufficient permissions"
	msgGRPCObligationsNotEnforceable = "authorization decision carries obligations not enforceable at this gate"
	msgGRPCAuthorizationDenied       = "authorization denied"
	msgGRPCMissingAuthMetadata       = "missing or invalid authorization metadata"
	msgGRPCPasswordResetRequired     = "password reset required before accessing this method"
	msgGRPCInvalidToken              = "invalid token"
	msgGRPCAuthnServiceUnavailable   = "authentication service unavailable"
	msgGRPCAuthzServiceUnavailable   = "authorization service unavailable"
	// msgGRPCResourceUnresolved is the PDP gate message when the method declares a
	// resource field selector but extraction STRUCTURALLY fails (field absent, wrong
	// kind, or non-proto message) — F3 fail-closed (#2207). An empty / non-UUID value
	// is NOT a structural failure: it is forwarded to the PDP (HTTP parity).
	msgGRPCResourceUnresolved = "resource field extraction failed; access denied"
)

// denyReason is the sealed, machine-readable reason carried in the
// google.rpc.ErrorInfo detail of every gRPC auth/authz denial (#2008 F6). It lets
// clients branch on a stable enum instead of parsing the English status message,
// which differs only by canonical code (three distinct authz failure modes all map
// to codes.PermissionDenied). The set is CLOSED: the unexported field + package-level
// values mean no caller outside this package can mint a reason, and deniedStatus only
// accepts a denyReason — a raw string can never reach the ErrorInfo.Reason slot.
type denyReason struct{ s string }

// String returns the wire spelling carried in ErrorInfo.Reason.
func (r denyReason) String() string { return r.s }

// denyReasonDomain is the google.rpc.ErrorInfo.Domain qualifying every reason in
// this package, so a client keys on (Domain, Reason) without colliding with other
// services' ErrorInfo reasons.
const denyReasonDomain = "gocell.authz.grpc"

// The closed set of gRPC auth/authz denial reasons. Every deny/auth-failure return
// point in this file routes through exactly one of these. authn (pre-PDP) and authz
// (PDP gate) reasons share one enum so the whole interceptor error model is uniform
// (no half-migrated bare status.Error).
var (
	reasonInvalidAuthMetadata     = denyReason{"INVALID_AUTH_METADATA"}
	reasonInvalidToken            = denyReason{"INVALID_TOKEN"}
	reasonAuthnServiceUnavailable = denyReason{"AUTHN_SERVICE_UNAVAILABLE"}
	reasonPasswordResetRequired   = denyReason{"PASSWORD_RESET_REQUIRED"}
	reasonAuthenticationRequired  = denyReason{"AUTHENTICATION_REQUIRED"}
	reasonNoPermissionMapping     = denyReason{"NO_PERMISSION_MAPPING"}
	reasonAuthzNotWired           = denyReason{"AUTHZ_NOT_WIRED"}
	reasonInsufficientPermissions = denyReason{"INSUFFICIENT_PERMISSIONS"}
	reasonObligationsUnsupported  = denyReason{"OBLIGATIONS_UNSUPPORTED"}
	reasonPDPUnavailable          = denyReason{"PDP_UNAVAILABLE"}
	reasonAuthorizationDenied     = denyReason{"AUTHORIZATION_DENIED"}
	// reasonResourceUnresolved is the F3 fail-closed reason for #2207: the method
	// declares a resource field selector but extraction STRUCTURALLY failed (field
	// absent, wrong kind, or req is not a proto.Message). An empty / non-UUID value is
	// forwarded to the PDP, not failed here. PII-safe: no extracted value enters
	// ErrorInfo.Metadata.
	reasonResourceUnresolved = denyReason{"RESOURCE_UNRESOLVED"}
)

// allDenyReasons registers every reason for anti-vacuity tests (uniqueness +
// non-empty spelling). A new reason MUST be added here or the registry test fails.
var allDenyReasons = []denyReason{
	reasonInvalidAuthMetadata, reasonInvalidToken, reasonAuthnServiceUnavailable,
	reasonPasswordResetRequired, reasonAuthenticationRequired, reasonNoPermissionMapping,
	reasonAuthzNotWired, reasonInsufficientPermissions, reasonObligationsUnsupported,
	reasonPDPUnavailable, reasonAuthorizationDenied, reasonResourceUnresolved,
}

// deniedStatus builds a gRPC status carrying a machine-readable google.rpc.ErrorInfo
// detail (#2008 F6): the canonical code + human message serve humans, while
// ErrorInfo.Reason (the sealed enum) + Domain + Metadata let clients reliably
// distinguish no-mapping / not-wired / denied / obligation / unavailable. Metadata
// carries only non-PII routing keys (method, and permission when known) — never the
// subject or token. If attaching the detail ever fails (not expected — ErrorInfo is a
// static proto), the bare status is returned so a denial is never downgraded.
func deniedStatus(code codes.Code, msg string, reason denyReason, md map[string]string) error {
	st := status.New(code, msg)
	enriched, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reason.String(),
		Domain:   denyReasonDomain,
		Metadata: md,
	})
	if err != nil {
		return st.Err()
	}
	return enriched.Err()
}

// denyMeta builds the non-PII ErrorInfo.Metadata for a denial: always the method,
// plus the required permission when the gate has resolved it. An empty permission
// is omitted (the pre-PDP authn denials have no permission context).
func denyMeta(method, permission string) map[string]string {
	md := map[string]string{"method": method}
	if permission != "" {
		md["permission"] = permission
	}
	return md
}

// AuthOption configures the auth interceptor.
type AuthOption func(*authConfig)

// PermissionResolver maps a full gRPC method name (/{Service}/{Method}) to the
// sealed authz.Permission it requires (#2008). ok=false means the method has NO
// permission mapping — under strict fail-closed the PDP gate DENIES such a method
// (a non-public RPC with no declared permission is a dead method, not authn-only).
// In production the registrar's PermissionForMethod is the single source (wired in
// chain.go); the contract overlay (endpoints.grpc.methods[].permission) is the
// authoring origin.
type PermissionResolver func(fullMethod string) (authz.Permission, bool)

// ResourceResolver maps a full gRPC method name (/{Service}/{Method}) to the
// request message field name (proto field, snake_case) whose string value should
// be extracted and forwarded as the PDP resource for per-message ownership authz
// (#2207). ok=false means the method has no resource field selector — the gate
// uses fullMethod as the resource (coarse, existing behavior). In production the
// registrar's ResourceFieldForMethod is the single source (wired in chain.go via
// WithResourceResolver, guarded by GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01).
type ResourceResolver func(fullMethod string) (field string, ok bool)

type authConfig struct {
	publicMethod        func(fullMethod string) bool
	passwordResetExempt func(fullMethod string) bool
	// authorizer is the PDP consulted by the per-method permission gate (#2008).
	// nil → the gate fail-closes (deny) at request time, mirroring the HTTP
	// RequirePermission "Authorizer absent → 403" guard.
	authorizer auth.Authorizer
	// permissionFor resolves a method to its required permission. nil → every
	// non-public method has no mapping → deny (strict fail-closed).
	permissionFor PermissionResolver
	// resourceFor resolves a method to the request-message field name to extract as
	// the PDP resource (#2207). nil → every method uses fullMethod as resource
	// (coarse, existing behavior). LAST-WINS: a single registrar-sourced resolver.
	resourceFor ResourceResolver
}

// WithPublicMethod adds pred to the predicates marking RPC methods that bypass
// authentication. Multiple WithPublicMethod options COMPOSE: a method is public
// if ANY installed predicate returns true — marking methods public is additive,
// not last-wins. The default (no predicate) is fail-closed: every method requires
// authentication. A nil predicate is a no-op.
//
// In production the registrar is the SINGLE source of the public-method set (#1675):
// runtime/grpc/interceptor/chain.go installs WithPublicMethod(reg.IsPublicMethod)
// (derived from each cell's endpoints.grpc.methods[] overlay), and
// GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01 forbids any other production reference to
// WithPublicMethod — so the composed union has exactly one member. Test harnesses
// may OR-in additional public methods for synthetic services not backed by a
// contract (e.g. a probe health service); the union semantics make that safe
// without weakening the production single-source.
func WithPublicMethod(pred func(fullMethod string) bool) AuthOption {
	return func(c *authConfig) {
		if pred == nil {
			return
		}
		if c.publicMethod == nil {
			c.publicMethod = pred
			return
		}
		prev := c.publicMethod
		c.publicMethod = func(m string) bool { return prev(m) || pred(m) }
	}
}

// WithPasswordResetExempt adds pred to the predicates marking RPC methods exempt
// from the password-reset gate (the gRPC analog of the HTTP route-based exempt
// matcher). Multiple WithPasswordResetExempt options COMPOSE: a method is exempt
// if ANY installed predicate returns true — marking methods exempt is additive,
// not last-wins. The default (no predicate) is fail-closed: a reset-required token
// is rejected on every method. A nil predicate is a no-op.
//
// In production the registrar is the SINGLE source of the password-reset-exempt set
// (#1382): runtime/grpc/interceptor/chain.go installs
// WithPasswordResetExempt(reg.IsPasswordResetExemptMethod) (derived from each cell's
// endpoints.grpc.methods[].passwordResetExempt overlay), and
// GRPC-PASSWORD-RESET-EXEMPT-WIRING-FUNNEL-01 forbids any other production reference
// to WithPasswordResetExempt — so the composed union has exactly one member. Test
// harnesses may OR-in additional exempt methods for synthetic services not backed by
// a contract; the union semantics make that safe without weakening the production
// single-source. (This OR-compose design mirrors WithPublicMethod (#1675); see its
// godoc for the rationale.)
func WithPasswordResetExempt(pred func(fullMethod string) bool) AuthOption {
	return func(c *authConfig) {
		if pred == nil {
			return
		}
		if c.passwordResetExempt == nil {
			c.passwordResetExempt = pred
			return
		}
		prev := c.passwordResetExempt
		c.passwordResetExempt = func(m string) bool { return prev(m) || pred(m) }
	}
}

// WithPDPAuthorizer installs the ABAC PDP the per-method permission gate consults
// (#2008). It is the gRPC analog of the HTTP composition-root
// bootstrap.WithPrimaryAuthorizer wiring: the composition root passes the same
// cell-provided Authorizer here so gRPC method authorization is the identical PDP
// decision as the HTTP route gates. A nil/typed-nil authorizer is a no-op (the
// field stays nil) and the gate fail-closes (deny) at request time — NOT a
// construction panic, because a server with zero permission-gated methods (all
// public or none) must still boot. In production the SOLE installer is chain.go
// (GRPC-PERMISSION-GATE-WIRING-FUNNEL-01).
func WithPDPAuthorizer(a auth.Authorizer) AuthOption {
	return func(c *authConfig) {
		if !validation.IsNilInterface(a) {
			c.authorizer = a
		}
	}
}

// WithPermissionResolver installs the method→permission resolver the PDP gate uses
// to look up each RPC's required action (#2008). In production the SOLE installer
// is chain.go, which wires the registrar's PermissionForMethod (derived from each
// cell's endpoints.grpc.methods[].permission overlay) — making the registrar the
// single runtime source of the method→permission map, the authorization sibling of
// the WithPublicMethod single source. A nil resolver is a no-op; the default (no
// resolver) is fail-closed — every non-public method has no mapping → deny.
// LAST-WINS (not OR-compose): like password-reset exemption, the permission map has
// a single source, so a second resolver would be a configuration conflict.
func WithPermissionResolver(r PermissionResolver) AuthOption {
	return func(c *authConfig) {
		if r != nil {
			c.permissionFor = r
		}
	}
}

// WithResourceResolver installs the method→resource-field resolver the PDP gate
// uses to extract the per-message resource for owner-scoped authz (#2207). When a
// method resolves to a field name, the interceptor extracts that field from the
// first received request message (unary: the only message; streaming: first RecvMsg)
// via protoreflect, canonicalizes it via ParseCanonicalUUID (UUID → canonical, else
// forwarded raw — exact HTTP RequirePermissionForResource parity), and forwards it as
// the PDP resource instead of fullMethod. F3 fail-closed: a STRUCTURAL extraction
// failure (not proto.Message, declared field absent, wrong kind) DENIES — it never
// falls back to fullMethod. A value-level case (empty / non-UUID) is forwarded to the
// PDP, not denied (so admin/operator who ignore the resource still pass). The SOLE installer
// is chain.go (GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01). A nil resolver is a no-op;
// the default (no resolver) is the coarse fallback (fullMethod as resource).
// LAST-WINS: like password-reset exemption, the resource map has a single source.
func WithResourceResolver(r ResourceResolver) AuthOption {
	return func(c *authConfig) {
		if r != nil {
			c.resourceFor = r
		}
	}
}

// UnaryAuth returns an interceptor that extracts a bearer token from the
// incoming metadata, verifies it via runtime/auth.AuthenticateBearer (the shared
// transport-agnostic core), applies the password-reset gate, and on success
// forwards the principal-enriched context to the handler. Failures map directly
// to gRPC status codes (Unauthenticated / Unavailable / PermissionDenied),
// mirroring the HTTP handleAuthRequest classification.
//
// For owner-scoped methods (#2207) the interceptor additionally extracts the
// resource field from req (F3 fail-closed: extraction failure → deny) and forwards
// it as the PDP resource instead of fullMethod.
//
// The verifier is required: a nil verifier is a wiring bug that fails fast at
// construction (programmer-error panic). For a security interceptor this is
// correct — the server must refuse to start rather than boot and reject every
// request, which would be indistinguishable from an outage.
func UnaryAuth(verifier auth.IntentTokenVerifier, opts ...AuthOption) grpc.UnaryServerInterceptor {
	if validation.IsNilInterface(verifier) {
		panic(panicregister.Approved("interceptor-auth-verifier-required",
			errcode.Assertion("interceptor.UnaryAuth: verifier is required")))
	}
	cfg := authConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (res any, retErr error) {
		// Stage-level panic guard (mirrors the guard in authorizeWithPrincipal): covers
		// the extractResourceForUnary / authorizePermission calls that run outside the
		// authorizeWithPrincipal recover scope (#1790).
		defer func() {
			if v := recover(); v != nil {
				res, retErr = nil, recoverGRPCPanic(ctx, "auth", info.FullMethod, v)
			}
		}()
		authCtx, p, err := authorizeWithPrincipal(ctx, cfg, verifier, info.FullMethod)
		if err != nil {
			return nil, err
		}
		// p == nil → public method (bypass granted by authorizeWithPrincipal). Skip
		// resource extraction and permission gate.
		if p != nil {
			// Owner-scoped resource extraction (#2207): run after authn so we have the
			// principal for logging, but before forwarding to the handler. F3 fail-closed.
			resource, denyErr := extractResourceForUnary(authCtx, cfg, p, info.FullMethod, req)
			if denyErr != nil {
				return nil, denyErr
			}
			if err := authorizePermission(authCtx, cfg, p, info.FullMethod, resource); err != nil {
				return nil, err
			}
		}
		return handler(authCtx, req)
	}
}

// authorizeWithPrincipal is the transport-shape-agnostic authn core shared by
// UnaryAuth and StreamAuth — the single source of the gRPC bearer-auth + authn
// decision across both transports. It returns the principal-enriched context and
// the authenticated Principal so callers can pass it to the permission gate
// (authorizePermission) with an appropriate resource.
//
// It applies the public-method bypass, extracts and verifies the bearer token via
// the shared runtime/auth.AuthenticateBearer core, and applies the password-reset
// gate. On any failure it returns a gRPC status error and a nil Principal (the
// context is returned unchanged on failure). For public methods it returns (ctx,
// nil, nil) — no principal is available.
//
// The whole auth stage runs OUTSIDE UnaryRecovery/StreamRecovery (Recovery wraps
// only the handler), so authorizeWithPrincipal installs ONE stage-level panic guard
// reusing the shared recoverGRPCPanic core (#1790): any panic from a predicate, the
// bearer verifier, or metadata parsing is collapsed into codes.Internal and logged
// (redacted) instead of escaping the chain unobserved by the outer Metrics/Tracing
// interceptors. The named returns exist solely so the deferred guard can rewrite the
// result on the panic path; the normal paths all return explicitly.
func authorizeWithPrincipal(
	ctx context.Context, cfg authConfig, verifier auth.IntentTokenVerifier, fullMethod string,
) (resultCtx context.Context, p *auth.Principal, err error) {
	resultCtx = ctx
	defer func() {
		if v := recover(); v != nil {
			resultCtx, p, err = ctx, nil, recoverGRPCPanic(ctx, "auth", fullMethod, v)
		}
	}()

	// Public-method bypass: a JWT-exempt RPC (#1675) skips token extraction
	// entirely, so the handler receives the ORIGINAL ctx with NO authenticated
	// principal. A public RPC's handler must not assume auth.PrincipalFromContext
	// yields a caller — it is anonymous by construction. A public method is never
	// permission-gated (Public ⊕ Permission are mutually exclusive in the overlay).
	if callPredicate(cfg.publicMethod, fullMethod) {
		//nolint:nilnil // public-method bypass: a JWT-exempt RPC has no principal and
		// no error — (ctx, nil, nil) is the documented signal callers branch on (p==nil).
		return ctx, nil, nil
	}

	token, ok := bearerFromMetadata(ctx)
	if !ok {
		return ctx, nil, deniedStatus(codes.Unauthenticated, msgGRPCMissingAuthMetadata,
			reasonInvalidAuthMetadata, denyMeta(fullMethod, ""))
	}

	authCtx, principal, verr := auth.AuthenticateBearer(ctx, verifier, token)
	if verr != nil {
		return ctx, nil, authErrorToStatus(verr, fullMethod)
	}

	if auth.PasswordResetBlocked(principal, callPredicate(cfg.passwordResetExempt, fullMethod)) {
		return ctx, nil, deniedStatus(codes.PermissionDenied, msgGRPCPasswordResetRequired,
			reasonPasswordResetRequired, denyMeta(fullMethod, ""))
	}

	return authCtx, principal, nil
}

// authorizePermission is the #2008/#2207 per-method PDP gate, run after
// authentication on the non-public path. It mirrors runtime/auth.RequirePermission's
// fail-closed decision order, adapted to gRPC status codes (there is no general
// errcode→codes mapper yet; the gate maps inline like authErrorToStatus). Decision order:
//
//  1. No permission mapping (resolver nil, or ok=false, or zero Permission) →
//     PermissionDenied. Strict fail-closed (#2008): a non-public RPC with no
//     declared permission is a dead method, NOT an authn-only one. This is a
//     configuration verdict independent of the caller, so it is checked first.
//  2. Principal absent / empty subject → Unauthenticated (defense-in-depth;
//     authentication already succeeded on this path).
//  3. Authorizer not wired → PermissionDenied (an unwired PDP must never permit).
//  4. Authorize error → Unavailable on KindUnavailable (policy store down), else
//     PermissionDenied.
//  5. Deny → PermissionDenied.
//  6. Allow with a non-zero obligation this coarse gate cannot discharge →
//     PermissionDenied (HTTP F5 parity: dropping a restricting obligation would
//     widen what the caller sees; the baseline carries zero obligations).
//
// resource is the PDP authz target for the Authorize call. For coarse methods it
// equals fullMethod (mirroring HTTP RequirePermission forwarding r.URL.Path). For
// owner-scoped methods the caller pre-extracts the per-message field value
// (extractResourceForMessage, resource.go, #2207) and supplies it here; F3
// fail-closed means extraction failure → DENY before reaching this function.
// PII note: resource may be a device UUID — it MUST NOT appear in denyMeta (which
// goes into wire ErrorInfo.Metadata). Only fullMethod and permission enter denyMeta.
func authorizePermission(ctx context.Context, cfg authConfig, p *auth.Principal, fullMethod, resource string) error {
	perm, ok := resolveMethodPermission(cfg.permissionFor, fullMethod)
	if !ok {
		// Misconfiguration (or a dead method): WARN so it is distinguishable from a
		// routine policy deny in operator logs.
		slog.WarnContext(ctx, "grpc authz: no permission mapping for method — denying (fail-closed)",
			slog.String("method", fullMethod))
		return deniedStatus(codes.PermissionDenied, msgGRPCNoPermissionMapping,
			reasonNoPermissionMapping, denyMeta(fullMethod, ""))
	}
	if p == nil || p.Subject == "" {
		return deniedStatus(codes.Unauthenticated, msgGRPCAuthRequired,
			reasonAuthenticationRequired, denyMeta(fullMethod, perm.String()))
	}
	if validation.IsNilInterface(cfg.authorizer) {
		slog.ErrorContext(ctx, "grpc authz: Authorizer not wired — denying (fail-closed)",
			slog.String("method", fullMethod), slog.String("subject", p.Subject), slog.String("permission", perm.String()))
		return deniedStatus(codes.PermissionDenied, msgGRPCAuthzNotWired,
			reasonAuthzNotWired, denyMeta(fullMethod, perm.String()))
	}
	dec, err := cfg.authorizer.Authorize(ctx, p.Subject, resource, perm.String())
	if err != nil {
		logGRPCAuthorizeError(ctx, err, fullMethod, p.Subject, perm.String())
		return pdpErrorToStatus(err, fullMethod, perm.String())
	}
	if !dec.IsAllow() {
		// Routine policy deny: INFO with the diagnostic reason (Deny()'s reason is
		// programmer-authored, never PII — observability.md / decision.go contract).
		slog.InfoContext(ctx, "grpc authz: permission denied by PDP",
			slog.String("method", fullMethod), slog.String("subject", p.Subject),
			slog.String("permission", perm.String()), slog.String("reason", dec.Reason()))
		return deniedStatus(codes.PermissionDenied, msgGRPCInsufficientPermissions,
			reasonInsufficientPermissions, denyMeta(fullMethod, perm.String()))
	}
	if obl := dec.Obligations(); !obl.IsZero() {
		slog.WarnContext(ctx, "grpc authz: Allow carries obligations not enforceable at this gate — denying (fail-closed)",
			slog.String("method", fullMethod), slog.String("subject", p.Subject), slog.String("permission", perm.String()))
		return deniedStatus(codes.PermissionDenied, msgGRPCObligationsNotEnforceable,
			reasonObligationsUnsupported, denyMeta(fullMethod, perm.String()))
	}
	return nil
}

// logGRPCAuthorizeError logs an Authorizer.Authorize error at the appropriate
// level (the gRPC sibling of runtime/auth.logAuthorizerError): a KindPermissionDenied
// (expected tenant-missing deny) is WARN; all others ERROR. The error is redacted
// before logging per observability.md §Redaction, so a PDP-store failure is
// diagnosable without leaking sensitive detail to the log sink.
func logGRPCAuthorizeError(ctx context.Context, err error, fullMethod, subject, permission string) {
	var ec *errcode.Error
	kind := errcode.KindInternal
	if errors.As(err, &ec) {
		kind = ec.Kind
	}
	args := []any{
		slog.Any("error", redaction.RedactError(err)),
		slog.Int("kind_status", kind.Status()),
		slog.String("method", fullMethod),
		slog.String("subject", subject),
		slog.String("permission", permission),
	}
	if kind == errcode.KindPermissionDenied {
		slog.WarnContext(ctx, "grpc authz: Authorizer.Authorize returned error", args...)
	} else {
		slog.ErrorContext(ctx, "grpc authz: Authorizer.Authorize returned error", args...)
	}
}

// resolveMethodPermission looks up the method's required permission, reporting
// ok=false for a nil resolver, an unmapped method, or a zero Permission (the
// last is defense-in-depth — the registrar resolves to a non-zero sealed value or
// fails fast at registration, so a zero here would be a programmer error).
func resolveMethodPermission(resolver PermissionResolver, fullMethod string) (authz.Permission, bool) {
	if resolver == nil {
		return authz.Permission{}, false
	}
	perm, ok := resolver(fullMethod)
	if !ok || perm.IsZero() {
		return authz.Permission{}, false
	}
	return perm, true
}

// pdpErrorToStatus classifies an Authorizer.Authorize error into a gRPC status with
// a machine-readable reason (#2008 F6), mirroring authErrorToStatus: a KindUnavailable
// policy-store outage surfaces as codes.Unavailable (reasonPDPUnavailable); every
// other error is an enumeration-safe codes.PermissionDenied (reasonAuthorizationDenied)
// — the Authorizer contract guarantees a non-Allow decision on error, so treating any
// error as deny is sound.
func pdpErrorToStatus(err error, fullMethod, permission string) error {
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Kind == errcode.KindUnavailable {
		return deniedStatus(codes.Unavailable, msgGRPCAuthzServiceUnavailable,
			reasonPDPUnavailable, denyMeta(fullMethod, permission))
	}
	return deniedStatus(codes.PermissionDenied, msgGRPCAuthorizationDenied,
		reasonAuthorizationDenied, denyMeta(fullMethod, permission))
}

// callPredicate invokes an externally-supplied auth predicate (public-method /
// password-reset-exempt), reporting false for a nil predicate (the fail-closed
// default). A panicking predicate needs no local recover here: authorize's
// stage-level guard (the auth stage runs outside Recovery) collapses it into
// codes.Internal, so this stays a pure dispatch.
func callPredicate(pred func(string) bool, method string) bool {
	if pred == nil {
		return false
	}
	return pred(method)
}

// bearerFromMetadata extracts the bearer token from the lowercase
// "authorization" metadata key. The scheme comparison is case-insensitive per
// RFC 7235 §2.1.
func bearerFromMetadata(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	vals := md.Get(authMetadataKey)
	if len(vals) == 0 {
		return "", false
	}
	// Multiple authorization values are ambiguous — there is no single
	// unambiguous bearer credential, so reject rather than silently pick the
	// first (F8 credential-confusion guard).
	if len(vals) > 1 {
		return "", false
	}
	scheme, token, found := strings.Cut(vals[0], " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

// authErrorToStatus classifies a verifier error into a gRPC status with a
// machine-readable reason (#2008 F6), mirroring the HTTP handleAuthRequest mapping:
// a KindUnavailable infra outage surfaces as codes.Unavailable
// (reasonAuthnServiceUnavailable); every other verification failure is an
// enumeration-safe codes.Unauthenticated (reasonInvalidToken). The general
// errcode.Kind → codes.Code table for handler-returned errors is a separate,
// later-PR concern.
func authErrorToStatus(err error, fullMethod string) error {
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Kind == errcode.KindUnavailable {
		return deniedStatus(codes.Unavailable, msgGRPCAuthnServiceUnavailable,
			reasonAuthnServiceUnavailable, denyMeta(fullMethod, ""))
	}
	return deniedStatus(codes.Unauthenticated, msgGRPCInvalidToken,
		reasonInvalidToken, denyMeta(fullMethod, ""))
}
