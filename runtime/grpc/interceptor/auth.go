package interceptor

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
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

// WithPublicMethod installs a predicate marking RPC methods that bypass
// authentication (e.g. health checks). The wiring of concrete public-method
// sets is a later-PR concern (tracked in backlog); the default (nil predicate)
// is fail-closed — every method requires authentication. Passing a nil
// predicate is a no-op; any previously installed predicate is retained.
func WithPublicMethod(pred func(fullMethod string) bool) AuthOption {
	return func(c *authConfig) {
		if pred != nil {
			c.publicMethod = pred
		}
	}
}

// WithPasswordResetExempt installs a predicate marking RPC methods exempt from
// the password-reset gate (the gRPC analog of the HTTP route-based exempt
// matcher). The default (nil predicate) is fail-closed — a reset-required token
// is rejected on every method. Passing a nil predicate is a no-op; any
// previously installed predicate is retained.
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
func authorize(ctx context.Context, cfg authConfig, verifier auth.IntentTokenVerifier, fullMethod string) (context.Context, error) {
	isPublic, err := callPredicate(ctx, cfg.publicMethod, fullMethod)
	if err != nil {
		return ctx, err
	}
	if isPublic {
		return ctx, nil
	}

	token, ok := bearerFromMetadata(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "missing or invalid authorization metadata")
	}

	ctx, p, err := auth.AuthenticateBearer(ctx, verifier, token)
	if err != nil {
		return ctx, authErrorToStatus(err)
	}

	exempt, err := callPredicate(ctx, cfg.passwordResetExempt, fullMethod)
	if err != nil {
		return ctx, err
	}
	if auth.PasswordResetBlocked(p, exempt) {
		return ctx, status.Error(codes.PermissionDenied, "password reset required before accessing this method")
	}

	return ctx, nil
}

// callPredicate invokes an externally-supplied auth predicate (public-method /
// password-reset-exempt) and converts any panic into a codes.Internal status.
// The Auth interceptor runs OUTSIDE UnaryRecovery (Recovery wraps only the
// handler), so without this guard a panicking predicate would escape the chain
// unobserved by the outer Metrics/Tracing interceptors and surface as an opaque
// transport error rather than a clean codes.Internal. A nil predicate reports
// false (the fail-closed default).
func callPredicate(ctx context.Context, pred func(string) bool, method string) (result bool, err error) {
	if pred == nil {
		return false, nil
	}
	defer func() {
		if v := recover(); v != nil {
			slog.ErrorContext(ctx, "grpc auth predicate panicked",
				slog.String("method", method),
				slog.Any("panic", redaction.RedactAny(v)),
			)
			result = false
			err = status.Error(codes.Internal, "internal server error")
		}
	}()
	return pred(method), nil
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
