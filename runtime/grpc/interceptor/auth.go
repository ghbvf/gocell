package interceptor

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
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
		if cfg.publicMethod != nil && cfg.publicMethod(info.FullMethod) {
			return handler(ctx, req)
		}

		token, ok := bearerFromMetadata(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid authorization metadata")
		}

		ctx, p, err := auth.AuthenticateBearer(ctx, verifier, token)
		if err != nil {
			return nil, authErrorToStatus(err)
		}

		exempt := cfg.passwordResetExempt != nil && cfg.passwordResetExempt(info.FullMethod)
		if auth.PasswordResetBlocked(p, exempt) {
			return nil, status.Error(codes.PermissionDenied, "password reset required before accessing this method")
		}

		return handler(ctx, req)
	}
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
