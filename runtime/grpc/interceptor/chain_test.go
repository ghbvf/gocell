package interceptor

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func panicHandler(context.Context, any) (any, error) { panic("boom") }

// nestRecovered wraps a handler so that Recovery (innermost) runs closest to it.
func nestRecovered(info *grpc.UnaryServerInfo, h grpc.UnaryHandler) grpc.UnaryHandler {
	return func(ctx context.Context, req any) (any, error) {
		return UnaryRecovery()(ctx, req, info, h)
	}
}

// TestChainOrderRecoveryInnermost verifies the load-bearing ordering property:
// because Recovery is innermost, it converts a handler panic into
// codes.Internal *before* the outer Metrics and Tracing interceptors observe
// the result — so they record a clean Internal rather than a raw panic.
func TestChainOrderRecoveryInnermost(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	t.Run("metrics observes recovery-converted Internal", func(t *testing.T) {
		coll := metrics.NewInMemoryGRPCCollector()
		_, err := UnaryMetrics(coll, clock.Real(), nil)(
			context.Background(), nil, info, nestRecovered(info, panicHandler))
		if status.Code(err) != codes.Internal {
			t.Fatalf("code = %v, want Internal", status.Code(err))
		}
		if got := coll.Count("_runtime", method, codes.Internal.String()); got != 1 {
			t.Fatalf("metrics Internal count = %d, want 1", got)
		}
	})

	t.Run("tracing observes recovery-converted error", func(t *testing.T) {
		tr := &recordingTracer{span: &recordingSpan{}}
		_, err := UnaryTracing(tr)(
			context.Background(), nil, info, nestRecovered(info, panicHandler))
		if status.Code(err) != codes.Internal {
			t.Fatalf("code = %v, want Internal", status.Code(err))
		}
		if tr.span.recordedErr == nil {
			t.Fatalf("tracing did not record the recovery-converted error")
		}
	})
}

func TestNewUnaryChain(t *testing.T) {
	// Smoke: composition must not panic and must return a usable ServerOption.
	opt := NewUnaryChain(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		CellResolver:    func(string) (string, bool) { return "", false },
		CellIDClosedSet: []string{"svc-cell"},
	})
	if opt == nil {
		t.Fatalf("NewUnaryChain returned nil ServerOption")
	}
	// It must be installable on a real server without panicking.
	_ = grpc.NewServer(opt)
}

// testSvc is a minimal gRPC service implementation used by F5 test only.
type testSvc struct{ handlerReached *bool }

func (s *testSvc) Do(ctx context.Context, req any) (any, error) {
	*s.handlerReached = true
	return &emptypb.Empty{}, nil
}

// testSvcDesc is a hand-crafted ServiceDesc that registers a single unary
// method without requiring protobuf generated code.
var testSvcDesc = grpc.ServiceDesc{
	ServiceName: "svc",
	HandlerType: (*interface{})(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Public",
			Handler: func(srv any, ctx context.Context, _ func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				s := srv.(*testSvc)
				if interceptor == nil {
					return s.Do(ctx, nil)
				}
				return interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Public"}, func(c context.Context, r any) (any, error) {
					return s.Do(c, r)
				})
			},
		},
	},
	Streams: []grpc.StreamDesc{},
}

// TestNewUnaryChain_AuthOptionsPassthrough asserts that AuthOptions from Deps
// are actually forwarded to UnaryAuth by NewUnaryChain. The test drives a real
// gRPC server built from NewUnaryChain's ServerOption. If deps.AuthOptions...
// is removed from the UnaryAuth call in chain.go, the WithPublicMethod
// predicate will not take effect and the unauthenticated request will be
// rejected as codes.Unauthenticated instead of reaching the handler.
func TestNewUnaryChain_AuthOptionsPassthrough(t *testing.T) {
	handlerReached := false
	deps := Deps{
		Collector: metrics.NewInMemoryGRPCCollector(),
		Clock:     clock.Real(),
		Verifier:  stubVerifier{},
		AuthOptions: []AuthOption{
			// Mark /svc/Public as public so no token is required.
			WithPublicMethod(func(m string) bool { return m == "/svc/Public" }),
		},
		CellResolver:    func(string) (string, bool) { return "", false },
		CellIDClosedSet: []string{"svc-cell"},
	}

	srv := grpc.NewServer(NewUnaryChain(deps))
	srv.RegisterService(&testSvcDesc, &testSvc{handlerReached: &handlerReached})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Invoke /svc/Public without any authorization metadata.
	callErr := conn.Invoke(context.Background(), "/svc/Public", &emptypb.Empty{}, &emptypb.Empty{})
	if callErr != nil {
		t.Fatalf("expected no error for public method without token, got %v (code=%v) — AuthOptions not forwarded",
			callErr, status.Code(callErr))
	}
	if !handlerReached {
		t.Fatalf("handler was not reached — WithPublicMethod option did not propagate through NewUnaryChain")
	}
}
