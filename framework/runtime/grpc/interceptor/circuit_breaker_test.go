package interceptor

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ─── stub Allower ─────────────────────────────────────────────────────────────

// stubAllower is a configurable Allower for circuit-breaker tests.
type stubAllower struct {
	allowed   bool
	doneErr   *error // if non-nil, the error passed to done() is stored here
	doneNil   bool   // true if done() was called with nil
	doneCalls int
}

func (s *stubAllower) Allow() (bool, func(error)) {
	if !s.allowed {
		return false, nil
	}
	return true, func(err error) {
		s.doneCalls++
		if err == nil {
			s.doneNil = true
		} else if s.doneErr != nil {
			*s.doneErr = err
		}
	}
}

// nilDoneAllower returns allowed=true with a nil done func (contract violation).
type nilDoneAllower struct{}

func (nilDoneAllower) Allow() (bool, func(error)) { return true, nil }

// cbFakeStream is a minimal grpc.ServerStream for circuit-breaker tests.
type cbFakeStream struct {
	ctx context.Context
}

func (f *cbFakeStream) Context() context.Context       { return f.ctx }
func (f *cbFakeStream) SendMsg(_ any) error            { return nil }
func (f *cbFakeStream) RecvMsg(_ any) error            { return nil }
func (f *cbFakeStream) SetHeader(_ metadata.MD) error  { return nil }
func (f *cbFakeStream) SendHeader(_ metadata.MD) error { return nil }
func (f *cbFakeStream) SetTrailer(_ metadata.MD)       {}

// ─── IsServerFailureCode table ───────────────────────────────────────────────

func TestIsServerFailureCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code codes.Code
		want bool
	}{
		// Failure codes: circuit-breaker counts these.
		{codes.Internal, true},
		{codes.Unknown, true},
		{codes.Unavailable, true},
		{codes.DataLoss, true},
		{codes.DeadlineExceeded, true},
		// Non-failure: client errors and permanent contract gaps.
		{codes.InvalidArgument, false},
		{codes.NotFound, false},
		{codes.PermissionDenied, false},
		{codes.Unauthenticated, false},
		{codes.Canceled, false},
		{codes.ResourceExhausted, false},
		{codes.Unimplemented, false}, // permanent contract gap, not health signal
		{codes.Aborted, false},
		{codes.OK, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.code.String(), func(t *testing.T) {
			t.Parallel()
			if got := isServerFailureCode(tc.code); got != tc.want {
				t.Errorf("isServerFailureCode(%v) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// ─── Unary circuit-breaker ───────────────────────────────────────────────────

func TestUnaryCircuitBreaker_NilCB_Passthrough(t *testing.T) {
	// nil cb → opt-in passthrough; handler must be called.
	t.Parallel()
	handlerCalled := false
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryCircuitBreaker(nil)(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { handlerCalled = true; return "ok", nil })
	if err != nil {
		t.Fatalf("nil cb: unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatalf("nil cb: handler was not called (must be passthrough)")
	}
}

func TestUnaryCircuitBreaker_Open_ReturnsUnavailable(t *testing.T) {
	t.Parallel()
	cb := &stubAllower{allowed: false}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryCircuitBreaker(cb)(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) {
			t.Fatal("handler must not be called when circuit is open")
			return "unreachable", errors.New("unreachable")
		})
	if err == nil {
		t.Fatal("open circuit: expected error, got nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("open circuit: code = %v, want Unavailable", got)
	}
}

func TestUnaryCircuitBreaker_Closed_Success_DoneNil(t *testing.T) {
	// Handler success → done(nil) is called.
	t.Parallel()
	cb := &stubAllower{allowed: true}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryCircuitBreaker(cb)(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("success: unexpected error: %v", err)
	}
	if !cb.doneNil {
		t.Errorf("success: done was not called with nil")
	}
	if cb.doneCalls != 1 {
		t.Errorf("done call count = %d, want 1", cb.doneCalls)
	}
}

func TestUnaryCircuitBreaker_Closed_ServerFailure_DoneErr(t *testing.T) {
	// Handler returns codes.Internal → done(non-nil) is called.
	t.Parallel()
	var capturedDoneErr error
	cb := &stubAllower{allowed: true, doneErr: &capturedDoneErr}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	handlerErr := status.Error(codes.Internal, "internal server error")
	_, err := UnaryCircuitBreaker(cb)(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return nil, handlerErr })
	// Verify the handler error is propagated (check by status code, not identity,
	// to avoid errorlint's "comparing with != will fail on wrapped errors" warning).
	if status.Code(err) != codes.Internal {
		t.Errorf("handler error not propagated: got code=%v, want Internal (err=%v)", status.Code(err), err)
	}
	if capturedDoneErr == nil {
		t.Errorf("done must be called with non-nil error for server failure code")
	}
	if cb.doneNil {
		t.Errorf("done was called with nil for a server failure code")
	}
}

func TestUnaryCircuitBreaker_Closed_ClientError_DoneNil(t *testing.T) {
	// Handler returns InvalidArgument (client error) → done(nil) is called (not a health signal).
	t.Parallel()
	cb := &stubAllower{allowed: true}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	handlerErr := status.Error(codes.InvalidArgument, "bad request")
	_, err := UnaryCircuitBreaker(cb)(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return nil, handlerErr })
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("handler error not propagated: got code=%v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
	if !cb.doneNil {
		t.Errorf("done must be called with nil for a client error (not a failure signal)")
	}
}

func TestUnaryCircuitBreaker_NilDone_FailOpen(t *testing.T) {
	// done == nil (contract violation) → fail-open (no-op), handler result propagated.
	t.Parallel()
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	resp, err := UnaryCircuitBreaker(nilDoneAllower{})(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("nil-done fail-open: unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("nil-done fail-open: resp = %v, want ok", resp)
	}
}

// ─── Stream circuit-breaker ───────────────────────────────────────────────────

func TestStreamCircuitBreaker_NilCB_Passthrough(t *testing.T) {
	t.Parallel()
	handlerCalled := false
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &cbFakeStream{ctx: context.Background()}
	err := StreamCircuitBreaker(nil)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error { handlerCalled = true; return nil })
	if err != nil {
		t.Fatalf("nil cb stream: unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatalf("nil cb stream: handler was not called")
	}
}

func TestStreamCircuitBreaker_Open_ReturnsUnavailable(t *testing.T) {
	t.Parallel()
	cb := &stubAllower{allowed: false}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &cbFakeStream{ctx: context.Background()}
	err := StreamCircuitBreaker(cb)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error {
			t.Fatal("stream handler must not be called when circuit is open")
			return errors.New("unreachable")
		})
	if err == nil {
		t.Fatal("open circuit stream: expected error, got nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("open circuit stream: code = %v, want Unavailable", got)
	}
}

func TestStreamCircuitBreaker_Closed_Success_DoneNil(t *testing.T) {
	t.Parallel()
	cb := &stubAllower{allowed: true}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &cbFakeStream{ctx: context.Background()}
	err := StreamCircuitBreaker(cb)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error { return nil })
	if err != nil {
		t.Fatalf("stream success: unexpected error: %v", err)
	}
	if !cb.doneNil {
		t.Errorf("stream success: done was not called with nil")
	}
}

func TestStreamCircuitBreaker_Closed_ServerFailure_DoneErr(t *testing.T) {
	t.Parallel()
	var capturedDoneErr error
	cb := &stubAllower{allowed: true, doneErr: &capturedDoneErr}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &cbFakeStream{ctx: context.Background()}
	handlerErr := status.Error(codes.Internal, "internal error")
	err := StreamCircuitBreaker(cb)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error { return handlerErr })
	// Verify propagation by status code to avoid errorlint "!=" warning.
	if status.Code(err) != codes.Internal {
		t.Errorf("stream handler error not propagated: got code=%v, want Internal (err=%v)", status.Code(err), err)
	}
	if capturedDoneErr == nil {
		t.Errorf("stream: done must be called with non-nil for server failure code")
	}
}
