package interceptor

// circuit_breaker.go — two-step circuit breaker interceptors (unary + stream).
//
// The Allower interface is declared here with a narrow footprint that matches
// the "allow + done" two-step pattern found in sony/gobreaker's
// TwoStepCircuitBreaker and go-kratos/aegis's CircuitBreaker. The interface
// is NOT imported from those packages — coupling would violate the
// runtime/grpc/interceptor isolation boundary. Structural compatibility is
// sufficient: any adapter wrapping the real library passes as Allower.
//
// # nil done guard
//
// If Allow() returns true but done is nil, the interceptor fails open (no-op)
// and logs a structured slog.Error. This is a circuit-breaker contract
// violation: a healthy CB must always provide a non-nil done callback when it
// opens the gate. The fail-open behavior (pass request through without
// reporting the outcome) is intentional — silently returning Unavailable would
// hide the misconfiguration and impact legitimate callers. The slog.Error is
// the operator signal.
//
// # server vs client failure classification
//
// isServerFailureCode classifies the handler's gRPC status code for the
// done() callback:
//
//   - Server failures (Internal, Unknown, Unavailable, DataLoss,
//     DeadlineExceeded): call done(errServerFailure) → circuit breaker
//     counts this as a health signal and opens faster.
//   - Unimplemented: permanent contract gap, not a transient health signal.
//   - ResourceExhausted: the server is overloaded, not unhealthy.
//   - Client errors (InvalidArgument, NotFound, etc.): handler ran fine.
//
// For client errors and the two carve-out codes (Unimplemented,
// ResourceExhausted), done(nil) is called — the circuit is not tripped.
//
// ref: sony/gobreaker TwoStepCircuitBreaker
// ref: go-kratos/aegis circuitbreaker

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Allower is the narrow two-step circuit-breaker interface.
// Allow returns (allowed=false, nil) when the circuit is open; it returns
// (true, done) when the circuit is closed or half-open. The caller must
// invoke done(err) exactly once — nil err signals success, non-nil err
// signals failure. A non-nil done MUST be returned whenever allowed==true;
// returning nil done is a contract violation and triggers fail-open + log.
type Allower interface {
	Allow() (allowed bool, done func(err error))
}

// errServerFailure is the sentinel error passed to the circuit-breaker's
// done() callback when the downstream handler returns a server-side failure
// code. It is unexported so callers cannot reference or match it; its only
// role is to signal "count this as a failure" to the circuit-breaker.
var errServerFailure = errors.New("server failure")

// isServerFailureCode reports whether the gRPC status code should be counted
// as a server-side health failure for circuit-breaker purposes.
//
// True: Internal, Unknown, Unavailable, DataLoss, DeadlineExceeded
//   - These indicate the server could not complete the request due to an
//     infrastructure or capacity problem — relevant health signals.
//
// False (deliberate carve-outs):
//   - Unimplemented: a permanent contract gap, not transient.
//   - ResourceExhausted: overload shed, not unhealthy.
//   - Everything else (client errors): the handler ran correctly.
func isServerFailureCode(c codes.Code) bool {
	switch c {
	case codes.Internal,
		codes.Unknown,
		codes.Unavailable,
		codes.DataLoss,
		codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// cbDoneErr derives the done() argument from a handler error:
// nil or a client-error status → done(nil); server failure → done(errServerFailure).
func cbDoneErr(err error) error {
	if err == nil {
		return nil
	}
	if isServerFailureCode(status.Code(err)) {
		return errServerFailure
	}
	// Client error or carve-out code: handler ran correctly.
	return nil
}

// UnaryCircuitBreaker returns a unary server interceptor that gates requests
// through cb. When cb is nil the interceptor is a transparent pass-through
// (opt-in protection: deployers without a circuit-breaker configured are not
// penalized).
func UnaryCircuitBreaker(cb Allower) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if cb == nil {
			// nil cb: opt-in protection not configured; pass through.
			return handler(ctx, req)
		}
		allowed, done := cb.Allow()
		if !allowed {
			return nil, status.Error(codes.Unavailable, "circuit open")
		}
		if done == nil {
			// Contract violation: allowed but no done callback. Fail open
			// so the request proceeds; the slog.Error surfaces the bug.
			slog.ErrorContext(ctx, "grpc circuit-breaker Allow() returned nil done — fail-open",
				"interceptor", "UnaryCircuitBreaker")
			return handler(ctx, req)
		}
		resp, err := handler(ctx, req)
		done(cbDoneErr(err))
		return resp, err
	}
}

// StreamCircuitBreaker returns a stream server interceptor that gates stream
// setup through cb. Semantics mirror UnaryCircuitBreaker: nil cb is a
// pass-through, nil done triggers fail-open + log.
func StreamCircuitBreaker(cb Allower) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if cb == nil {
			// nil cb: opt-in protection not configured; pass through.
			return handler(srv, ss)
		}
		allowed, done := cb.Allow()
		if !allowed {
			return status.Error(codes.Unavailable, "circuit open")
		}
		if done == nil {
			// Contract violation: allowed but no done callback. Fail open.
			slog.ErrorContext(ss.Context(), "grpc circuit-breaker Allow() returned nil done — fail-open",
				"interceptor", "StreamCircuitBreaker")
			return handler(srv, ss)
		}
		err := handler(srv, ss)
		done(cbDoneErr(err))
		return err
	}
}
