package interceptor

// errcode_mapping.go — centralized errcode.Kind → grpc/codes.Code projection
// for the GoCell gRPC interceptor chain (PR-12 #1155).
//
// pkg/errcode MUST NOT import google.golang.org/grpc (it would pollute kernel/
// transitively). The mapping therefore lives here, in runtime/grpc/interceptor,
// which is the single transport-side concern owner. This mirrors the Kratos
// pattern (ref: go-kratos/kratos errors GRPCStatus) adapted as a standalone
// interceptor because GoCell's *errcode.Error is not a proto-generated type.
//
// # 4xx / 5xx redaction discipline
//
// Client errors (errcode.Kind.IsClient() == true, 4xx) use the errcode message
// directly — it is a const literal (MESSAGE-CONST-LITERAL-01) and therefore
// wire-safe. Server errors (5xx) use msgInternalServerError — the same generic
// message the HTTP 5xx path emits — so internal details never reach the wire.
// This mirrors framework/pkg/httputil/response.go 5xx projection.
//
// ref: go-kratos/kratos errors GRPCStatus
// ref: go-kratos/kratos transport/grpc/interceptor.go

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// msgInternalServerError is the generic server-error message emitted on the gRPC
// wire for all 5xx errcode.Errors (and any unknown non-status error). It must be
// a const literal so MESSAGE-CONST-LITERAL-01 is satisfied.
const msgInternalServerError = "internal server error"

// toGRPCCode maps an errcode.Kind to the canonical grpc/codes.Code.
//
// Every Kind constant is listed as an EXPLICIT case (GRPC-ERRCODE-MAPPING-01):
// a default clause alone would silently absorb a newly-added Kind. The mapping
// table is fixed (spec-frozen in PR-12 #1155) — changes require updating both
// this switch and the archtest exhaustiveness guard.
//
// cyclop: the function has 14 explicit Kind cases. This is a pure lookup switch
// with no logic branching; the high case count is inherent to the complete Kind
// enumeration and cannot be split without defeating the GRPC-ERRCODE-MAPPING-01
// exhaustiveness requirement (archtest scans this exact switch by AST). The
// project limit (≤ 15) is deliberately exceeded here by exactly one: a 14-case
// switch scores 15 (14 cases + 1 base). This is an intentional carve-out aligned
// with the PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 precedent.
//
//nolint:cyclop // 14-case exhaustive Kind switch required by GRPC-ERRCODE-MAPPING-01; complexity is inherent to the enum size
func toGRPCCode(k errcode.Kind) codes.Code {
	switch k {
	case errcode.KindInternal:
		return codes.Internal
	case errcode.KindInvalid:
		return codes.InvalidArgument
	case errcode.KindUnauthenticated:
		return codes.Unauthenticated
	case errcode.KindPermissionDenied:
		return codes.PermissionDenied
	case errcode.KindNotFound:
		return codes.NotFound
	case errcode.KindConflict:
		return codes.Aborted
	case errcode.KindUnprocessable:
		return codes.InvalidArgument
	case errcode.KindGone:
		return codes.NotFound
	case errcode.KindPayloadTooLarge:
		return codes.ResourceExhausted
	case errcode.KindRateLimited:
		return codes.ResourceExhausted
	case errcode.KindClientClosed:
		return codes.Canceled
	case errcode.KindDeadlineExceeded:
		return codes.DeadlineExceeded
	case errcode.KindUnavailable:
		return codes.Unavailable
	case errcode.KindNotImplemented:
		return codes.Unimplemented
	default:
		// Unknown future Kind: fail-closed as Internal.
		return codes.Internal
	}
}

// errToStatus converts a handler error into a gRPC status error:
//
//   - nil → nil (pass-through).
//   - Already-status errors whose code is not Unknown → pass through unchanged.
//     Auth and Recovery interceptors already produce a grpc status; double-mapping
//     would overwrite e.g. codes.Unauthenticated with codes.Internal.
//   - *errcode.Error: map via toGRPCCode. 4xx use the errcode message (wire-safe
//     const literal); 5xx use msgInternalServerError (no internal detail on wire).
//   - Any other error → codes.Internal with msgInternalServerError (fail-closed).
//
// # PublicDetails parity (backlogged)
//
// Only the errcode message is forwarded on the gRPC wire today. errcode PublicDetails
// (WithDetails(PublicString/PublicInt/…) attrs) are intentionally NOT carried in
// google.rpc.Status.Details — fail-safe default while the structured-detail encoding
// is unspecified. 4xx-details parity with HTTP is tracked in #2482.
//
// Do NOT implement GRPCStatus() on *errcode.Error: that would bypass this
// interceptor's 5xx redaction (5xx messages would reach the wire unredacted).
func errToStatus(err error) error {
	if err == nil {
		return nil
	}
	// Pass through already-status errors whose code is not Unknown.
	// status.FromError returns ok=true for any status-carrying error; Unknown is
	// the synthetic code grpc-go assigns to non-status errors, which is exactly
	// the *errcode.Error case we want to remap.
	if s, ok := status.FromError(err); ok {
		if s.Code() != codes.Unknown {
			return err
		}
	}
	// *errcode.Error: apply Kind→codes mapping with 4xx/5xx redaction.
	var ec *errcode.Error
	if errors.As(err, &ec) {
		code := toGRPCCode(ec.Kind)
		msg := buildStatusMessage(ec)
		return status.Error(code, msg)
	}
	// Context cancellation / deadline are NORMAL outcomes (client cancel, graceful
	// drain via StreamDrain, per-RPC timeout) — NOT server failures. Map them to the
	// canonical codes.Canceled / codes.DeadlineExceeded instead of the generic
	// Internal fallback, so the outer Metrics/Tracing/CircuitBreaker interceptors do
	// not count a cancellation as a server-side failure (neither code is in
	// isServerFailureCode). status.FromContextError is the gRPC-idiomatic mapper;
	// guard with errors.Is so non-context errors still fall through to the fail-closed
	// Internal below (FromContextError would otherwise map them to Unknown).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	// Unknown non-errcode error: fail-closed to Internal with generic message.
	return status.Error(codes.Internal, msgInternalServerError)
}

// buildStatusMessage returns the wire-safe message for an errcode.Error.
// Client errors (4xx) carry the const errcode message; server errors (5xx)
// use the generic message to prevent leaking internal state.
func buildStatusMessage(ec *errcode.Error) string {
	if ec.Kind.IsClient() {
		// 4xx: the errcode message is a const literal (MESSAGE-CONST-LITERAL-01),
		// safe to forward to the client.
		return ec.Message
	}
	// 5xx: never leak internal details to the wire.
	return msgInternalServerError
}

// UnaryErrcodeMap returns a unary server interceptor that maps the handler's
// returned error to a grpc status via errToStatus. It is the gRPC analog of the
// HTTP path's errcode→HTTP status projection (httputil/response.go). Place it
// just OUTSIDE UnaryRecovery (Recovery is innermost; ErrcodeMap sits one layer
// out) so that Recovery's codes.Internal result passes through this interceptor
// as an already-status error (codes != Unknown) and is not double-mapped.
//
// ref: go-kratos/kratos errors GRPCStatus
func UnaryErrcodeMap() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		return resp, errToStatus(err)
	}
}

// StreamErrcodeMap returns a streaming server interceptor that maps the handler's
// returned error to a grpc status via errToStatus — the streaming analog of
// UnaryErrcodeMap. Placed just outside StreamRecovery in the chain.
func StreamErrcodeMap() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return errToStatus(handler(srv, ss))
	}
}
