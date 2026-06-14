package interceptor

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// authMetadataKey is the lowercase gRPC metadata key carrying the bearer token,
// per the go-grpc-middleware / RFC 7235 convention. gRPC metadata keys are
// always lowercase, so the HTTP "Authorization" header getter cannot be reused.
const authMetadataKey = "authorization"

// AuthOption configures the auth interceptor.
type AuthOption func(*authConfig)

type authConfig struct {
	publicMethod        func(fullMethod string) bool
	passwordResetExempt func(fullMethod string) bool
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

// WithPasswordResetExempt installs a predicate marking RPC methods exempt from
// the password-reset gate (the gRPC analog of the HTTP route-based exempt
// matcher). The default (nil predicate) is fail-closed — a reset-required token
// is rejected on every method. Passing a nil predicate is a no-op; any
// previously installed predicate is retained.
//
// NOTE the deliberate asymmetry with WithPublicMethod: this is LAST-WINS (a later
// non-nil predicate replaces the earlier one), NOT OR-compose. Password-reset
// exemption has a single source — there is no always-on registrar predicate to
// union with — so multiple sources would be a configuration conflict, not an
// additive set. WithPublicMethod composes (OR) precisely because the registrar
// (#1675) is an always-present second source.
func WithPasswordResetExempt(pred func(fullMethod string) bool) AuthOption {
	return func(c *authConfig) {
		if pred != nil {
			c.passwordResetExempt = pred
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
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := authorize(ctx, cfg, verifier, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// authorize is the transport-shape-agnostic auth core shared by UnaryAuth and
// StreamAuth (PR-10 #1153) — the single source of the gRPC bearer-auth decision
// across both transports. It applies the public-method bypass, extracts and
// verifies the bearer token via the shared runtime/auth.AuthenticateBearer core,
// and applies the password-reset gate. On success it returns the
// principal-enriched context and a nil error; on any failure it returns a gRPC
// status error (the context is returned unchanged on the failure paths). The
// caller forwards the returned context to the handler (wrapping the stream for
// the streaming path).
//
// The whole auth stage runs OUTSIDE UnaryRecovery/StreamRecovery (Recovery wraps
// only the handler), so authorize installs ONE stage-level panic guard reusing the
// shared recoverGRPCPanic core (#1790): any panic from a predicate, the bearer
// verifier, or metadata parsing is collapsed into codes.Internal and logged
// (redacted) instead of escaping the chain unobserved by the outer Metrics/Tracing
// interceptors. This is the single chokepoint — no per-callsite guard can be
// forgotten — so the inner helpers (callPredicate) stay panic-naive. The named
// returns exist solely so the deferred guard can rewrite the result on the panic
// path; the normal paths all return explicitly.
func authorize(
	ctx context.Context, cfg authConfig, verifier auth.IntentTokenVerifier, fullMethod string,
) (resultCtx context.Context, err error) {
	resultCtx = ctx
	defer func() {
		if v := recover(); v != nil {
			resultCtx, err = ctx, recoverGRPCPanic(ctx, "auth", fullMethod, v)
		}
	}()

	// Public-method bypass: a JWT-exempt RPC (#1675) skips token extraction
	// entirely, so the handler receives the ORIGINAL ctx with NO authenticated
	// principal. A public RPC's handler must not assume auth.PrincipalFromContext
	// yields a caller — it is anonymous by construction. (Optional-token "upgrade
	// if present" is out of scope until the #2008 gRPC PDP wiring.)
	if callPredicate(cfg.publicMethod, fullMethod) {
		return ctx, nil
	}

	token, ok := bearerFromMetadata(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "missing or invalid authorization metadata")
	}

	authCtx, p, verr := auth.AuthenticateBearer(ctx, verifier, token)
	if verr != nil {
		return ctx, authErrorToStatus(verr)
	}

	if auth.PasswordResetBlocked(p, callPredicate(cfg.passwordResetExempt, fullMethod)) {
		return ctx, status.Error(codes.PermissionDenied, "password reset required before accessing this method")
	}

	return authCtx, nil
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

// authErrorToStatus classifies a verifier error into a gRPC status, mirroring
// the HTTP handleAuthRequest mapping: a KindUnavailable infra outage surfaces as
// codes.Unavailable; every other verification failure is an enumeration-safe
// codes.Unauthenticated. The general errcode.Kind → codes.Code table for
// handler-returned errors is a separate, later-PR concern.
func authErrorToStatus(err error) error {
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Kind == errcode.KindUnavailable {
		return status.Error(codes.Unavailable, "authentication service unavailable")
	}
	return status.Error(codes.Unauthenticated, "invalid token")
}
