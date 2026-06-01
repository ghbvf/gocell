package bootstrap

// grpc_listener_test.go — TDD coverage for WithGRPCListener + phase7b serve +
// phase10 stage2 gRPC drain (#1148 / GAP-1 PR-5).
//
// Cases:
//   1. nil verifier (bare + typed) → phase0 fail-fast ErrGRPCVerifierMissing, no socket bound
//   2. happy path: bufconn serve → in-flight RPC → ctx cancel → graceful drain → clean Run
//   3. HTTP + gRPC concurrent serve; gRPC serve error propagates through phase9
//   4. invalid TLS config → phase7b error surfaces (HTTP drained, no leak)
//   5. drain budget exceeded by a blocking RPC → hard-stop, teardown_grpc_drain error
//   6. multiple gRPC listeners share one cached collector (no duplicate registration)

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/kernel/assembly"
	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
)

const grpcTestBufSize = 1024 * 1024

// newGRPCTestVerifier returns a non-nil verifier that accepts everything; the
// happy path exempts the health method via WithPublicMethod so the verifier is
// never actually consulted, but WithGRPCListener requires a non-nil verifier.
func newGRPCTestVerifier() kauth.IntentTokenVerifier { return &bootstrapTestVerifier{} }

// healthRegister returns a WithGRPCService callback registering the standard
// grpc_health_v1 service reporting SERVING.
func healthRegister() func(grpc.ServiceRegistrar) {
	return func(reg grpc.ServiceRegistrar) {
		hs := health.NewServer()
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
		grpc_health_v1.RegisterHealthServer(reg, hs)
	}
}

// exemptHealthMethod marks the Health/Check method public so the auth
// interceptor does not require a bearer token in tests that exercise serve/drain.
func exemptHealthMethod() interceptor.AuthOption {
	return interceptor.WithPublicMethod(func(fullMethod string) bool {
		return fullMethod == grpc_health_v1.Health_Check_FullMethodName
	})
}

// dialBufconn dials a bufconn listener with plaintext credentials.
func dialBufconn(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	return cc
}

// minimalGRPCAssembly returns a runnable assembly with no cells (demo durability).
func minimalGRPCAssembly(t *testing.T, id string) *assembly.CoreAssembly {
	t.Helper()
	return assembly.New(clock.Real(), assembly.Config{ID: id, DurabilityMode: outbox.DurabilityDemo})
}

// healthListenerOpt declares the framework HealthListener (required by phase0)
// on a fresh loopback socket.
func healthListenerOpt(t *testing.T) Option {
	t.Helper()
	healthLn := newLocalListener(t)
	return WithListener(cell.HealthListener, healthLn.Addr().String(),
		[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(healthLn))
}

// --- Case 1: phase0 fail-fast on nil verifier ------------------------------

func TestWithGRPCListener_NilVerifier_Phase0FailFast(t *testing.T) {
	cases := []struct {
		name     string
		verifier kauth.IntentTokenVerifier
	}{
		{"bare_nil", nil},
		{"typed_nil", (*bootstrapTestVerifier)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(clock.Real(), WithGRPCListener(":0", tc.verifier))
			err := b.Run(context.Background())
			require.Error(t, err)
			assert.ErrorContains(t, err, string(errcode.ErrGRPCVerifierMissing),
				"phase0 must reject nil verifier with ErrGRPCVerifierMissing")
		})
	}
}

// --- Case 2: happy path serve → graceful drain -----------------------------

func TestWithGRPCListener_HappyPath_ServeThenGracefulStop(t *testing.T) {
	lis := bufconn.Listen(grpcTestBufSize)
	asm := minimalGRPCAssembly(t, "grpc-happy")

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(":0", newGRPCTestVerifier(),
			WithGRPCListenerNet(lis),
			WithGRPCListenerTLS(adaptersgrpc.TLSConfig{AllowInsecure: true}),
			WithGRPCAuthOptions(exemptHealthMethod()),
			WithGRPCService(healthRegister()),
		),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

	// In-flight RPC succeeds while serving.
	testwait.External(t, "grpc-serving", func() bool {
		rctx, rcancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer rcancel()
		resp, err := grpc_health_v1.NewHealthClient(cc).Check(rctx, &grpc_health_v1.HealthCheckRequest{})
		return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
	}, testtime.EventuallyDefault, testtime.MediumPoll, "grpc health did not become SERVING")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "clean ctx-cancel shutdown must return nil")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return after ctx cancel (gRPC drain hung)")
	}
}

// --- Case 3: HTTP + gRPC concurrent serve; gRPC error propagates -----------

func TestBootstrap_HTTPAndGRPC_ConcurrentServe_ErrorPropagation(t *testing.T) {
	// A pre-closed bufconn listener makes grpcServer.Serve return immediately
	// with a non-graceful error, which must propagate through phase9 → Run.
	lis := bufconn.Listen(grpcTestBufSize)
	require.NoError(t, lis.Close())

	asm := minimalGRPCAssembly(t, "grpc-errprop")
	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(":0", newGRPCTestVerifier(),
			WithGRPCListenerNet(lis),
			WithGRPCListenerTLS(adaptersgrpc.TLSConfig{AllowInsecure: true}),
			WithGRPCService(healthRegister()),
		),
		WithShutdownTimeout(testtime.D2s),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()

	select {
	case err := <-done:
		require.Error(t, err, "gRPC serve error must propagate to Run")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return on gRPC serve error")
	}
}

// --- Case 4: invalid TLS config surfaces as a phase7b error ----------------

func TestWithGRPCListener_TLSConfigInvalid_Phase7Error(t *testing.T) {
	lis := bufconn.Listen(grpcTestBufSize)
	asm := minimalGRPCAssembly(t, "grpc-badtls")
	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(":0", newGRPCTestVerifier(),
			WithGRPCListenerNet(lis),
			// CertPEM without KeyPEM → adapter Config.validate V4 fails.
			WithGRPCListenerTLS(adaptersgrpc.TLSConfig{CertPEM: []byte("not-a-key")}),
			WithGRPCService(healthRegister()),
		),
		WithShutdownTimeout(testtime.D2s),
	)

	err := b.Run(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, string(adaptersgrpc.ErrAdapterGRPCConfigInvalid),
		"invalid gRPC TLS config must surface ErrAdapterGRPCConfigInvalid")
}

// --- Case 5: drain budget exceeded → hard stop -----------------------------

func TestGRPCDrain_BudgetExceeded_HardStop(t *testing.T) {
	lis := bufconn.Listen(grpcTestBufSize)
	asm := minimalGRPCAssembly(t, "grpc-drainbudget")

	release := make(chan struct{})
	started := make(chan struct{})
	blockingSvc := func(reg grpc.ServiceRegistrar) {
		reg.RegisterService(&grpc.ServiceDesc{
			ServiceName: "test.Blocking",
			HandlerType: (*any)(nil),
			Methods: []grpc.MethodDesc{{
				MethodName: "Block",
				Handler: func(_ any, ctx context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
					close(started)
					<-release
					return &grpc_health_v1.HealthCheckResponse{}, nil
				},
			}},
		}, struct{}{})
	}

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(":0", newGRPCTestVerifier(),
			WithGRPCListenerNet(lis),
			WithGRPCListenerTLS(adaptersgrpc.TLSConfig{AllowInsecure: true}),
			WithGRPCAuthOptions(interceptor.WithPublicMethod(func(string) bool { return true })),
			WithGRPCService(blockingSvc),
			WithGRPCListenerShutdownTimeout(testtime.D50ms),
		),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

	// Fire the blocking RPC and wait for the handler to enter.
	go func() {
		_ = cc.Invoke(context.Background(), "/test.Blocking/Block",
			&grpc_health_v1.HealthCheckRequest{}, &grpc_health_v1.HealthCheckResponse{})
	}()
	select {
	case <-started:
	case <-time.After(testtime.D5s):
		t.Fatal("blocking RPC handler never started")
	}

	// Cancel → stage2 drain with the tiny per-listener budget → hard stop.
	cancel()
	select {
	case err := <-done:
		require.Error(t, err, "drain budget exceeded must surface a timeout error")
	case <-time.After(testtime.D5s):
		close(release)
		t.Fatal("Run did not return after drain budget exceeded")
	}
	close(release)
}

// --- Case 6: multiple gRPC listeners share one cached collector ------------

func TestWithGRPCListener_MultipleListeners_CollectorReused(t *testing.T) {
	spy := &registrationSpy{}
	asm := minimalGRPCAssembly(t, "grpc-multi")

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithMetricsProvider(spy),
		healthListenerOpt(t),
		WithGRPCListener(":0", newGRPCTestVerifier(),
			WithGRPCListenerNet(bufconn.Listen(grpcTestBufSize)),
			WithGRPCListenerTLS(adaptersgrpc.TLSConfig{AllowInsecure: true}),
			WithGRPCAuthOptions(exemptHealthMethod()),
			WithGRPCService(healthRegister()),
		),
		WithGRPCListener(":0", newGRPCTestVerifier(),
			WithGRPCListenerNet(bufconn.Listen(grpcTestBufSize)),
			WithGRPCListenerTLS(adaptersgrpc.TLSConfig{AllowInsecure: true}),
			WithGRPCAuthOptions(exemptHealthMethod()),
			WithGRPCService(healthRegister()),
		),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Let startup complete past phase7b, then shut down.
	testwait.External(t, "grpc-multi-up", func() bool {
		return countOccurrences(spy.counters(), "grpc_server_requests_total") >= 1
	}, testtime.EventuallyDefault, testtime.MediumPoll, "grpc collector never registered")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return")
	}

	assert.Equal(t, 1, countOccurrences(spy.counters(), "grpc_server_requests_total"),
		"two gRPC listeners must share ONE cached collector (no duplicate registration)")
}

func countOccurrences(ss []string, target string) int {
	n := 0
	for _, s := range ss {
		if s == target {
			n++
		}
	}
	return n
}
