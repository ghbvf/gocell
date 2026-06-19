package interceptor

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	runtimegrpc "github.com/ghbvf/gocell/framework/runtime/grpc"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
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
		// Direct interceptor invocation (not via newUnaryChain): a nil closed set
		// is valid here and resolves to the _runtime sentinel — this case asserts
		// the recovery-converted code/label, not cell attribution.
		_, err := UnaryMetrics(coll, clock.Real(), nil)(
			context.Background(), nil, info, nestRecovered(info, panicHandler),
		)
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
			context.Background(), nil, info, nestRecovered(info, panicHandler),
		)
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
	opt := newUnaryChain(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		CellIDClosedSet: []string{"svc-cell"},
	}, runtimegrpc.NewServiceRegistrar())
	if opt == nil {
		t.Fatalf("newUnaryChain returned nil ServerOption")
	}
	// It must be installable on a real server without panicking.
	_ = grpc.NewServer(opt)
}

// TestNewUnaryChainNilRegistrarPanics asserts the same-instance fail-closed
// guard: a chain without a registrar would silently attribute every RPC to the
// runtime sentinel, so newUnaryChain panics at construction (#1152 F1). In
// production NewServerInterceptors always mints a non-nil registrar (#1752); this
// white-box test exercises the residual defensive guard directly.
func TestNewUnaryChainNilRegistrarPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("newUnaryChain with a nil registrar must panic")
		}
	}()
	_ = newUnaryChain(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		CellIDClosedSet: []string{"svc-cell"},
	}, nil) // nil registrar → fail-closed panic.
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

// streamTestSvcDesc is a hand-crafted ServiceDesc with two server-stream methods
// (one declared public, one not) for the stream live-path test. grpc-go applies the
// chained StreamServerInterceptor (from newStreamChain) around StreamDesc.Handler,
// so the handler is the raw body — auth runs before it.
var streamTestSvcDesc = grpc.ServiceDesc{
	ServiceName: "svc",
	HandlerType: (*interface{})(nil),
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "PublicStream",
			ServerStreams: true,
			Handler:       func(srv any, _ grpc.ServerStream) error { *srv.(*testSvc).handlerReached = true; return nil },
		},
		{
			StreamName:    "PrivateStream",
			ServerStreams: true,
			Handler:       func(srv any, _ grpc.ServerStream) error { *srv.(*testSvc).handlerReached = true; return nil },
		},
	},
}

// TestNewStreamChain_RegistrarPublicMethodExempts is the stream sibling of
// TestNewUnaryChain_RegistrarPublicMethodExempts (#1675 review F4): it proves the
// stream chain installs WithPublicMethod(reg.IsPublicMethod) so a registrar-declared
// public STREAM method bypasses auth, while an undeclared stream method is authed
// (fail-closed). Without the registrar wiring in stream.go this test fails.
func TestNewStreamChain_RegistrarPublicMethodExempts(t *testing.T) {
	handlerReached := false
	reg := runtimegrpc.NewServiceRegistrar()
	drain := runtimegrpc.NewDrainSignal()
	deps := Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		CellIDClosedSet: []string{"svc-cell"},
	}
	srv := grpc.NewServer(newStreamChain(deps, reg, drain))
	reg.BindServer(srv)
	spec := cell.GRPCServiceSpec{
		ContractID:    "grpc.svc.v1",
		CellID:        "svc-cell",
		PublicMethods: []string{"/svc/PublicStream"}, // PrivateStream intentionally omitted
		Listener:      cell.PrimaryListener,
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&streamTestSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	openStream := func(method string) error {
		st, serr := conn.NewStream(context.Background(), &grpc.StreamDesc{ServerStreams: true}, method)
		if serr != nil {
			return serr
		}
		return st.RecvMsg(&emptypb.Empty{}) // EOF on normal close; status error on auth failure
	}

	// Undeclared stream method, no token → fail-closed (Unauthenticated), handler not reached.
	if err := openStream("/svc/PrivateStream"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("undeclared stream method must be authed (Unauthenticated), got code=%v err=%v", status.Code(err), err)
	}
	if handlerReached {
		t.Fatalf("private stream handler must not run when auth blocks")
	}

	// Registrar-declared public stream method, no token → bypass, handler reached.
	if err := openStream("/svc/PublicStream"); status.Code(err) == codes.Unauthenticated {
		t.Fatalf("registrar-declared public stream must bypass auth without a token, got Unauthenticated: %v", err)
	}
	if !handlerReached {
		t.Fatalf("public stream handler was not reached — registrar public-method wiring did not exempt /svc/PublicStream")
	}
}

// TestNewUnaryChain_AuthOptionsPassthrough asserts that AuthOptions from Deps are
// forwarded to UnaryAuth by newUnaryChain (via authChainOptions). It
// drives WithPasswordResetExempt — NOT WithPublicMethod — because #1675 made the
// registrar the single source of the public-method set. WithPublicMethod and
// WithPasswordResetExempt both use OR-compose (union) semantics: deps.AuthOptions
// predicates and registrar predicates are unioned, not overridden — any predicate
// returning true widens the respective set. If deps.AuthOptions is dropped from the
// UnaryAuth call, the exempt predicate stops taking effect and the reset-required
// token is rejected as PermissionDenied instead of reaching the handler.
func TestNewUnaryChain_AuthOptionsPassthrough(t *testing.T) {
	handlerReached := false
	// Registers a permission-gated spec, so declare a wired gate (#2008 F1).
	reg := runtimegrpc.NewServiceRegistrar(runtimegrpc.WithPermissionGate(true))
	deps := Deps{
		Collector: metrics.NewInMemoryGRPCCollector(),
		Clock:     clock.Real(),
		Verifier:  stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}},
		// #2008: the non-public method also passes the PDP gate, so wire an allowing
		// authorizer (the chain installs WithPermissionResolver(reg.PermissionForMethod)
		// + WithPDPAuthorizer(deps.Authorizer)); the permission mapping is registered
		// on reg below.
		Authorizer: stubAuthorizer{dec: mustAllow()},
		AuthOptions: []AuthOption{
			// Exempt /svc/Public from the password-reset gate.
			WithPasswordResetExempt(func(m string) bool { return m == "/svc/Public" }),
		},
		CellIDClosedSet: []string{"svc-cell"},
	}

	srv := grpc.NewServer(newUnaryChain(deps, reg))
	reg.BindServer(srv)
	if err := reg.Register(cell.GRPCServiceSpec{
		ContractID:        "grpc.svc.v1",
		CellID:            "svc-cell",
		Listener:          cell.PrimaryListener,
		MethodPermissions: map[string]string{"/svc/Public": authz.PermDeviceCommand().String()},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&testSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

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

	// Invoke /svc/Public WITH a bearer token whose principal requires a password
	// reset; the exempt option (forwarded via deps.AuthOptions) must let it reach
	// the handler instead of being blocked with PermissionDenied.
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer t")
	callErr := conn.Invoke(ctx, "/svc/Public", &emptypb.Empty{}, &emptypb.Empty{})
	if callErr != nil {
		t.Fatalf("expected no error for reset-exempt method, got %v (code=%v) — AuthOptions not forwarded",
			callErr, status.Code(callErr))
	}
	if !handlerReached {
		t.Fatalf("handler was not reached — WithPasswordResetExempt option did not propagate through newUnaryChain")
	}
}

// TestNewServerInterceptors_PermissionGate_EndToEnd is the production-path capstone
// for #2008: it wires the gRPC server through NewServerInterceptors (the SOLE
// production entry, which builds both chains and installs
// WithPermissionResolver(reg.PermissionForMethod) + WithPDPAuthorizer(deps.Authorizer)),
// registers a service whose method carries a permission overlay (exactly as cellgen
// emits MethodPermissions into cell_gen.go), and dials it over a real bufconn. A
// caller whose token grants the role passes the PDP gate and reaches the handler; a
// caller without it is denied PermissionDenied — proving the
// contract→registrar→interceptor→PDP funnel end-to-end through the real production
// wiring (not the package-private newUnaryChain).
func TestNewServerInterceptors_PermissionGate_EndToEnd(t *testing.T) {
	const requiredRole = "operator"
	handlerReached := false

	bundle := NewServerInterceptors(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        rolesFromTokenVerifier{},
		Authorizer:      roleGateAuthorizer{role: requiredRole},
		CellIDClosedSet: []string{"svc-cell"},
	})
	reg := bundle.Registrar()
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	if err := reg.Register(cell.GRPCServiceSpec{
		ContractID:        "grpc.svc.v1",
		CellID:            "svc-cell",
		Listener:          cell.PrimaryListener,
		MethodPermissions: map[string]string{"/svc/Public": authz.PermDeviceCommand().String()},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&testSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

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

	// Allow: the token grants the required role → the PDP gate permits → handler runs.
	allowCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+requiredRole)
	if err := conn.Invoke(allowCtx, "/svc/Public", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("authorized caller (role=%s) must reach handler, got %v (code=%v)",
			requiredRole, err, status.Code(err))
	}
	if !handlerReached {
		t.Fatalf("handler was not reached for the authorized caller")
	}

	// Deny: a token granting a different role → PDP gate denies → PermissionDenied,
	// handler not reached again.
	handlerReached = false
	denyCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer guest")
	err = conn.Invoke(denyCtx, "/svc/Public", &emptypb.Empty{}, &emptypb.Empty{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthorized caller must be PermissionDenied, got %v (code=%v)", err, status.Code(err))
	}
	if handlerReached {
		t.Fatalf("handler must not run for the unauthorized caller")
	}
}

// TestNewServerInterceptors_StreamPermissionGate_EndToEnd is the stream sibling of
// TestNewServerInterceptors_PermissionGate_EndToEnd (#2008): it wires the gRPC server
// through NewServerInterceptors and exercises a server-streaming method declared with a
// permission overlay. A caller whose token grants the required role opens the stream and
// reaches the handler; a caller without the role is denied PermissionDenied at stream-open.
func TestNewServerInterceptors_StreamPermissionGate_EndToEnd(t *testing.T) {
	const requiredRole = "operator"
	handlerReached := false

	bundle := NewServerInterceptors(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        rolesFromTokenVerifier{},
		Authorizer:      roleGateAuthorizer{role: requiredRole},
		CellIDClosedSet: []string{"svc-cell"},
	})
	reg := bundle.Registrar()
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	spec := cell.GRPCServiceSpec{
		ContractID: "grpc.svc.stream.v1",
		CellID:     "svc-cell",
		Listener:   cell.PrimaryListener,
		// PrivateStream requires the operator permission — mirrors the cellgen-emitted overlay.
		MethodPermissions: map[string]string{"/svc/PrivateStream": authz.PermDeviceCommand().String()},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&streamTestSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

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

	openStream := func(bearer string) error {
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+bearer)
		st, serr := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/svc/PrivateStream")
		if serr != nil {
			return serr
		}
		return st.RecvMsg(&emptypb.Empty{}) // EOF when handler completes; status error on gate denial
	}

	// Allow: token grants the required role → PDP permits → handler reached.
	// RecvMsg returns io.EOF (codes.Unknown) on clean server-side stream close, which is
	// the success path — the gate did not deny. Assert the code is NOT PermissionDenied.
	allowErr := openStream(requiredRole)
	if status.Code(allowErr) == codes.PermissionDenied {
		t.Fatalf("authorized stream caller (role=%s) must not be PermissionDenied, got %v",
			requiredRole, allowErr)
	}
	if !handlerReached {
		t.Fatalf("handler was not reached for the authorized stream caller")
	}

	// Deny: token grants a different role → PDP denies → PermissionDenied at stream-open.
	handlerReached = false
	recvErr := openStream("guest")
	if status.Code(recvErr) != codes.PermissionDenied {
		t.Fatalf("unauthorized stream caller must be PermissionDenied, got %v (code=%v)",
			recvErr, status.Code(recvErr))
	}
	if handlerReached {
		t.Fatalf("handler must not run for the unauthorized stream caller")
	}
}

// TestNewServerInterceptors_PasswordResetExempt_EndToEnd is the production-path
// capstone for #1382: it wires the gRPC server through NewServerInterceptors (the
// SOLE production entry) and registers a service whose method carries a
// PasswordResetExemptMethods overlay alongside a MethodPermissions overlay (a
// password-reset-exempt method is non-public and still needs a permission — the two
// are orthogonal and must coexist). A caller bearing a token with
// PasswordResetRequired:true reaches the declared-exempt method and is blocked on
// a non-exempt gated method — proving the contract→registrar→interceptor→reset-gate
// funnel end-to-end through the real production wiring.
func TestNewServerInterceptors_PasswordResetExempt_EndToEnd(t *testing.T) {
	// testSvcDesc has one unary method "/svc/Public"; we use it for the exempt method.
	// We add a second ServiceDesc with one method for the non-exempt one.
	resetSvcDesc := grpc.ServiceDesc{
		ServiceName: "resetsvc",
		HandlerType: (*interface{})(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Reset",
				Handler: func(srv any, ctx context.Context, _ func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
					s := srv.(*testSvc)
					if interceptor == nil {
						return s.Do(ctx, nil)
					}
					return interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/resetsvc/Reset"}, func(c context.Context, r any) (any, error) {
						return s.Do(c, r)
					})
				},
			},
			{
				MethodName: "Other",
				Handler: func(srv any, ctx context.Context, _ func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
					s := srv.(*testSvc)
					if interceptor == nil {
						return s.Do(ctx, nil)
					}
					return interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/resetsvc/Other"}, func(c context.Context, r any) (any, error) {
						return s.Do(c, r)
					})
				},
			},
		},
		Streams: []grpc.StreamDesc{},
	}

	handlerReached := false
	bundle := NewServerInterceptors(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}},
		Authorizer:      stubAuthorizer{dec: mustAllow()},
		CellIDClosedSet: []string{"svc-cell"},
	})
	reg := bundle.Registrar()
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	if err := reg.Register(cell.GRPCServiceSpec{
		ContractID:                 "grpc.resetsvc.v1",
		CellID:                     "svc-cell",
		Listener:                   cell.PrimaryListener,
		PasswordResetExemptMethods: []string{"/resetsvc/Reset"},
		MethodPermissions: map[string]string{
			"/resetsvc/Reset": authz.PermDeviceCommand().String(),
			"/resetsvc/Other": authz.PermDeviceCommand().String(),
		},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&resetSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

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

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer t")

	// Exempt method: reset-required token must reach the handler (gate bypassed).
	if err := conn.Invoke(ctx, "/resetsvc/Reset", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("exempt method must be reached by reset-required token, got %v (code=%v)",
			err, status.Code(err))
	}
	if !handlerReached {
		t.Fatalf("handler was not reached for the exempt method")
	}

	// Non-exempt gated method: reset-required token must be blocked (PermissionDenied).
	handlerReached = false
	denyErr := conn.Invoke(ctx, "/resetsvc/Other", &emptypb.Empty{}, &emptypb.Empty{})
	if status.Code(denyErr) != codes.PermissionDenied {
		t.Fatalf("non-exempt method must be PermissionDenied for reset-required token, got %v (code=%v)",
			denyErr, status.Code(denyErr))
	}
	if handlerReached {
		t.Fatalf("handler must not run for the non-exempt method with reset-required token")
	}
}

// TestNewServerInterceptors_StreamPasswordResetExempt_EndToEnd is the stream sibling
// of TestNewServerInterceptors_PasswordResetExempt_EndToEnd (#1382): it wires the
// gRPC server through NewServerInterceptors and exercises a server-streaming method
// declared as password-reset-exempt alongside a MethodPermissions overlay. A caller
// bearing a token with PasswordResetRequired:true reaches the declared-exempt stream
// method and is blocked on a non-exempt gated stream method — proving the
// contract→registrar→interceptor→reset-gate funnel on the stream path end-to-end.
func TestNewServerInterceptors_StreamPasswordResetExempt_EndToEnd(t *testing.T) {
	// resetStreamSvcDesc has two server-streaming methods: ResetStream (exempt) and
	// OtherStream (non-exempt), matching the ServiceDesc used in the unary equivalent.
	resetStreamSvcDesc := grpc.ServiceDesc{
		ServiceName: "resetsvc",
		HandlerType: (*interface{})(nil),
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "ResetStream",
				ServerStreams: true,
				Handler:       func(srv any, _ grpc.ServerStream) error { *srv.(*testSvc).handlerReached = true; return nil },
			},
			{
				StreamName:    "OtherStream",
				ServerStreams: true,
				Handler:       func(srv any, _ grpc.ServerStream) error { *srv.(*testSvc).handlerReached = true; return nil },
			},
		},
	}

	handlerReached := false
	bundle := NewServerInterceptors(Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}},
		Authorizer:      stubAuthorizer{dec: mustAllow()},
		CellIDClosedSet: []string{"svc-cell"},
	})
	reg := bundle.Registrar()
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	if err := reg.Register(cell.GRPCServiceSpec{
		ContractID:                 "grpc.resetsvc.stream.v1",
		CellID:                     "svc-cell",
		Listener:                   cell.PrimaryListener,
		PasswordResetExemptMethods: []string{"/resetsvc/ResetStream"},
		MethodPermissions: map[string]string{
			"/resetsvc/ResetStream": authz.PermDeviceCommand().String(),
			"/resetsvc/OtherStream": authz.PermDeviceCommand().String(),
		},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&resetStreamSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

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

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer t")

	openStream := func(method string) error {
		st, serr := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, method)
		if serr != nil {
			return serr
		}
		return st.RecvMsg(&emptypb.Empty{}) // EOF on handler return; status error on gate denial
	}

	// Exempt stream method: reset-required token must reach the handler (gate bypassed).
	// RecvMsg returns io.EOF (codes.Unknown) on clean close — assert NOT PermissionDenied.
	exemptErr := openStream("/resetsvc/ResetStream")
	if status.Code(exemptErr) == codes.PermissionDenied {
		t.Fatalf("exempt stream method must not be PermissionDenied for reset-required token, got %v", exemptErr)
	}
	if !handlerReached {
		t.Fatalf("handler was not reached for the exempt stream method")
	}

	// Non-exempt gated stream method: reset-required token must be blocked (PermissionDenied).
	handlerReached = false
	denyErr := openStream("/resetsvc/OtherStream")
	if status.Code(denyErr) != codes.PermissionDenied {
		t.Fatalf("non-exempt stream method must be PermissionDenied for reset-required token, got %v (code=%v)",
			denyErr, status.Code(denyErr))
	}
	if handlerReached {
		t.Fatalf("handler must not run for the non-exempt stream method with reset-required token")
	}
}

// TestNewUnaryChain_RegistrarPublicMethodExempts is the live-path proof for #1675:
// chain.go installs WithPublicMethod(reg.IsPublicMethod), so a method declared
// public via GRPCServiceSpec.PublicMethods bypasses auth WITHOUT a token. No
// WithPublicMethod is passed via Deps.AuthOptions — the registrar is the sole
// public-method source. This proves the overlay → registrar → interceptor chain
// is wired (not dead config).
func TestNewUnaryChain_RegistrarPublicMethodExempts(t *testing.T) {
	handlerReached := false
	reg := runtimegrpc.NewServiceRegistrar()
	deps := Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        stubVerifier{},
		CellIDClosedSet: []string{"svc-cell"},
	}

	srv := grpc.NewServer(newUnaryChain(deps, reg))
	reg.BindServer(srv)
	spec := cell.GRPCServiceSpec{
		ContractID:    "grpc.svc.v1",
		CellID:        "svc-cell",
		Listener:      cell.PrimaryListener,
		PublicMethods: []string{"/svc/Public"},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&testSvcDesc, &testSvc{handlerReached: &handlerReached})
		},
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}

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

	// /svc/Public is declared public via the registrar; invoke without any token.
	callErr := conn.Invoke(context.Background(), "/svc/Public", &emptypb.Empty{}, &emptypb.Empty{})
	if callErr != nil {
		t.Fatalf("registrar-declared public method must bypass auth without a token, got %v (code=%v)",
			callErr, status.Code(callErr))
	}
	if !handlerReached {
		t.Fatalf("handler was not reached — registrar public-method wiring did not exempt /svc/Public")
	}
}
