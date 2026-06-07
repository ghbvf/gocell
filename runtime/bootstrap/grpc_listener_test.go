package bootstrap

// grpc_listener_test.go — TDD coverage for WithGRPCListener + phase7b serve +
// phase10 stage2 gRPC drain (#1148 / GAP-1 PR-5).
//
// Layering note: this is a _test.go file, exempt from the runtime/ → adapters/
// import ban (LAYER-03 `!**/runtime/**/*_test.go`). It therefore plays the
// composition-root role — building the interceptor chain and the adapters/grpc
// server — exactly as cmd/ or examples/ would, then handing the ready server to
// WithGRPCListener (which only sees the GRPCServer interface).
//
// Cases:
//   1. nil server (bare + typed) → phase0 fail-fast ErrGRPCServerMissing, no socket
//   2. happy path: bufconn serve → in-flight RPC → ctx cancel → graceful drain → clean Run
//   3. HTTP + gRPC concurrent serve; gRPC serve error propagates through phase9
//   4. two gRPC listeners both serve and both drain on shutdown
//   5. drain budget exceeded by a blocking RPC → hard stop, clean-or-error Run

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
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
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

const grpcTestBufSize = 1024 * 1024

// buildAdapterServer constructs an adapters/grpc.Server with the full unary
// interceptor chain wired (as the composition root would), registering the
// caller-supplied services via the Form B Registrar path. authPublic, when
// non-nil, marks methods exempt from the auth interceptor so RPCs can be issued
// without a bearer token.
func buildAdapterServer(
	t *testing.T,
	authPublic func(fullMethod string) bool,
	register func(grpc.ServiceRegistrar),
) *adaptersgrpc.Server {
	t.Helper()
	var authOpts []interceptor.AuthOption
	if authPublic != nil {
		authOpts = append(authOpts, interceptor.WithPublicMethod(authPublic))
	}
	reg := runtimegrpc.NewServiceRegistrar()
	chain := interceptor.NewUnaryChain(interceptor.Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        &bootstrapTestVerifier{}, // non-nil: NewUnaryChain panics on nil
		AuthOptions:     authOpts,
		Registrar:       reg, // Option 3: shared registrar (#1152)
		CellIDClosedSet: []string{"bootstrap-test-cell"},
	})
	srv, err := adaptersgrpc.New(adaptersgrpc.Config{
		Addr:          ":0",
		TLS:           adaptersgrpc.TLSConfig{AllowInsecure: true},
		ServerOptions: []grpc.ServerOption{chain},
		Registrar:     reg,
	})
	require.NoError(t, err)
	if register != nil {
		// Register via Form B callback using a synthetic spec (grpc_listener_test
		// plays the composition-root role; it may import adapters/grpc and call
		// Registrar() directly — identical to how cmd/ would wire things pre-cell).
		spec := cell.GRPCServiceSpec{
			ContractID: "grpc.listener.test.v1",
			CellID:     "_listener-test",
			Listener:   cell.PrimaryListener,
			Register:   register,
		}
		require.NoError(t, srv.Registrar().Register(spec))
	}
	return srv
}

// registerHealth registers grpc_health_v1 reporting SERVING.
func registerHealth(reg grpc.ServiceRegistrar) {
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(reg, hs)
}

// healthPublic exempts the Health/Check method from auth.
func healthPublic(fullMethod string) bool {
	return fullMethod == grpc_health_v1.Health_Check_FullMethodName
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

func minimalGRPCAssembly(t *testing.T, id string) *assembly.CoreAssembly {
	t.Helper()
	return assembly.New(clock.Real(), assembly.Config{ID: id, DurabilityMode: outbox.DurabilityDemo})
}

// healthListenerOpt declares the framework HealthListener (required by phase0).
func healthListenerOpt(t *testing.T) Option {
	t.Helper()
	healthLn := newLocalListener(t)
	return WithListener(cell.HealthListener, healthLn.Addr().String(),
		[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(healthLn))
}

// stubGRPCServer is a no-op GRPCServer for phase0 config-validation tests that
// must pass the nil-server guard but never actually serve.
type stubGRPCServer struct{}

func (stubGRPCServer) Serve(context.Context, net.Listener) error { return nil }
func (stubGRPCServer) Close(context.Context) error               { return nil }
func (stubGRPCServer) Registrar() GRPCServiceRegistrar           { return &noopGRPCServiceRegistrar{} }
func (stubGRPCServer) Probes() []healthz.Probe                   { return nil }

// probeGRPCServer is a GRPCServer exposing one readiness probe, for the
// expandGRPCServerProbes collection test (#1152). checkErr is what the probe's
// Check returns (nil = healthy).
type probeGRPCServer struct {
	stubGRPCServer
	probeName healthz.ProbeName
	checkErr  error
}

func (s probeGRPCServer) Probes() []healthz.Probe {
	return []healthz.Probe{healthz.NewProbe(s.probeName, func(context.Context) error { return s.checkErr })}
}

// grpcReadyChecker returns the single collected checker for name, or nil.
func grpcReadyChecker(b *Bootstrap, name healthz.ProbeName) func(context.Context) error {
	var fn func(context.Context) error
	count := 0
	for _, hc := range b.healthCheckers {
		if hc.name == name {
			fn = hc.fn
			count++
		}
	}
	if count != 1 {
		return nil // 0 = not collected; >1 = duplicate (would collide at aggregator)
	}
	return fn
}

// TestExpandGRPCServerProbes_CollectsReadyProbe asserts a declared gRPC server's
// readiness probe is collected into b.healthCheckers at Run() start, so the
// phase5 drainProbes registers it onto the aggregator (the gRPC server is wired
// via WithGRPCListener, not WithManagedResource, so its Probes() are otherwise
// uncollected — the orphaned-probe bug this PR fixes).
func TestExpandGRPCServerProbes_CollectsReadyProbe(t *testing.T) {
	const name healthz.ProbeName = "grpc_ready"
	b := New(clock.Real(),
		WithGRPCListener(cell.PrimaryListener, probeGRPCServer{probeName: name}, ":0"))
	b.expandGRPCServerProbes()

	fn := grpcReadyChecker(b, name)
	require.NotNil(t, fn, "grpc_ready must be collected exactly once into healthCheckers")
	require.NoError(t, fn(context.Background()), "single healthy server → grpc_ready healthy")
}

// TestExpandGRPCServerProbes_MultiListenerComposite asserts two gRPC listeners
// collapse to ONE grpc_ready checker (no duplicate-name collision) whose check
// is the AND of every server — an unhealthy listener surfaces, not silently
// dropped.
func TestExpandGRPCServerProbes_MultiListenerComposite(t *testing.T) {
	const name healthz.ProbeName = "grpc_ready"
	downErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "grpc: not serving")
	b := New(clock.Real(),
		WithGRPCListener(cell.PrimaryListener, probeGRPCServer{probeName: name}, ":0"),
		WithGRPCListener(cell.InternalListener, probeGRPCServer{probeName: name, checkErr: downErr}, ":0"))
	b.expandGRPCServerProbes()

	fn := grpcReadyChecker(b, name)
	require.NotNil(t, fn, "two listeners must collapse to exactly one grpc_ready checker")
	require.Error(t, fn(context.Background()),
		"grpc_ready must be unhealthy when any gRPC server is not serving (AND semantics)")
}

// noopGRPCServiceRegistrar is a no-op GRPCServiceRegistrar for config-validation tests.
type noopGRPCServiceRegistrar struct{}

func (n *noopGRPCServiceRegistrar) Register(_ cell.GRPCServiceSpec) error { return nil }

// --- Case 1: phase0 fail-fast on nil server -------------------------------

func TestWithGRPCListener_NilServer_Phase0FailFast(t *testing.T) {
	cases := []struct {
		name   string
		server GRPCServer
	}{
		{"bare_nil", nil},
		{"typed_nil", (*adaptersgrpc.Server)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(clock.Real(), WithGRPCListener(cell.PrimaryListener, tc.server, ":0"))
			err := b.Run(context.Background())
			require.Error(t, err)
			assert.ErrorContains(t, err, string(errcode.ErrGRPCServerMissing),
				"phase0 must reject nil gRPC server with ErrGRPCServerMissing")
		})
	}
}

// --- Case 2: happy path serve → graceful drain ----------------------------

func TestWithGRPCListener_HappyPath_ServeThenGracefulStop(t *testing.T) {
	lis := bufconn.Listen(grpcTestBufSize)
	srv := buildAdapterServer(t, healthPublic, registerHealth)
	asm := minimalGRPCAssembly(t, "grpc-happy")

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srv, ":0", WithGRPCListenerNet(lis)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

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

// TestWithGRPCListener_ReadyzReportsGRPCReady is the integration proof that the
// grpc_ready probe reaches /readyz through the real Run → phase5 drainProbes →
// health aggregator → HTTP handler chain (not just the expandGRPCServerProbes
// helper, #1737 F6). It starts a HealthListener + a bufconn gRPC listener, drives
// Run, and asserts /readyz?verbose lists grpc_ready once the server is serving.
func TestWithGRPCListener_ReadyzReportsGRPCReady(t *testing.T) {
	lis := bufconn.Listen(grpcTestBufSize)
	srv := buildAdapterServer(t, healthPublic, registerHealth)
	asm := minimalGRPCAssembly(t, "grpc-readyz")
	healthLn := newLocalListener(t)
	const verboseToken = "readyz-test-token"

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithListener(cell.HealthListener, healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(healthLn)),
		WithGRPCListener(cell.PrimaryListener, srv, ":0", WithGRPCListenerNet(lis)),
		WithHealthRoutes(WithReadyzVerboseToken(verboseToken)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testtime.D5s):
			t.Fatal("Run did not return after ctx cancel")
		}
	}()

	healthAddr := healthLn.Addr().String()
	// Wait until /readyz reports overall ready (grpc serving + probe registered).
	testwait.External(t, "readyz-ready", func() bool {
		resp, err := readyzVerbose(t, healthAddr, verboseToken)
		if err != nil {
			return false
		}
		defer closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "/readyz did not become ready")

	resp, err := readyzVerbose(t, healthAddr, verboseToken)
	require.NoError(t, err)
	defer closeBody(t, resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "grpc_ready",
		"/readyz?verbose must list grpc_ready (proves Run→drainProbes→aggregator→handler chain)")
}

// readyzVerbose GETs /readyz?verbose=true with the verbose token header.
func readyzVerbose(t *testing.T, addr, token string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/readyz?verbose=true", addr), nil)
	require.NoError(t, err)
	req.Header.Set("X-Readyz-Token", token)
	return testHTTPClient.Do(req)
}

// --- Case 3: HTTP + gRPC concurrent serve; gRPC error propagates ----------

func TestBootstrap_HTTPAndGRPC_ConcurrentServe_ErrorPropagation(t *testing.T) {
	// A pre-closed bufconn listener makes grpcServer.Serve return immediately
	// with a non-graceful error, which must propagate through phase9 → Run.
	lis := bufconn.Listen(grpcTestBufSize)
	require.NoError(t, lis.Close())

	srv := buildAdapterServer(t, healthPublic, registerHealth)
	asm := minimalGRPCAssembly(t, "grpc-errprop")
	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srv, ":0", WithGRPCListenerNet(lis)),
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

// --- Case 4: two gRPC listeners both serve and both drain -----------------

func TestWithGRPCListener_TwoListeners_BothServeAndDrain(t *testing.T) {
	lisA := bufconn.Listen(grpcTestBufSize)
	lisB := bufconn.Listen(grpcTestBufSize)
	asm := minimalGRPCAssembly(t, "grpc-two")

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, buildAdapterServer(t, healthPublic, registerHealth), ":0", WithGRPCListenerNet(lisA)),
		WithGRPCListener(cell.InternalListener, buildAdapterServer(t, healthPublic, registerHealth), ":0", WithGRPCListenerNet(lisB)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	for _, lis := range []*bufconn.Listener{lisA, lisB} {
		cc := dialBufconn(t, lis)
		testwait.External(t, "grpc-serving", func() bool {
			rctx, rcancel := context.WithTimeout(context.Background(), testtime.D2s)
			defer rcancel()
			resp, err := grpc_health_v1.NewHealthClient(cc).Check(rctx, &grpc_health_v1.HealthCheckRequest{})
			return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
		}, testtime.EventuallyDefault, testtime.MediumPoll, "a gRPC listener did not become SERVING")
		_ = cc.Close()
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "both gRPC listeners must drain cleanly")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// --- Case 5: drain budget exceeded → hard stop ----------------------------

func TestGRPCDrain_BudgetExceeded_HardStop(t *testing.T) {
	lis := bufconn.Listen(grpcTestBufSize)
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
					// Block past the graceful budget, but honor the hard-stop
					// cancellation (grpcServer.Stop cancels the RPC ctx) so the
					// handler goroutine does not leak — a well-behaved handler.
					select {
					case <-release:
					case <-ctx.Done():
					}
					return &grpc_health_v1.HealthCheckResponse{}, nil
				},
			}},
		}, struct{}{})
	}
	srv := buildAdapterServer(t, func(string) bool { return true }, blockingSvc)
	asm := minimalGRPCAssembly(t, "grpc-drainbudget")

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srv, ":0",
			WithGRPCListenerNet(lis),
			WithGRPCListenerShutdownGrace(testtime.D50ms),
		),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

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
	// Close returns the deadline error, surfaced as teardown_grpc_drain, so Run
	// returns non-nil — the contract is that a blocked RPC cannot wedge shutdown
	// past the per-listener budget AND the exceeded budget is reported.
	cancel()
	select {
	case err := <-done:
		require.Error(t, err, "drain budget exceeded must surface a non-nil shutdown error")
	case <-time.After(testtime.D5s):
		close(release)
		t.Fatal("Run did not return after drain budget exceeded (hard stop failed)")
	}
	close(release)
}

// --- Case 6: gRPC bind failure drains the already-serving HTTP --------------

func TestWithGRPCListener_BindFailure_DrainsHTTP(t *testing.T) {
	// Occupy a TCP port, then declare a gRPC listener on the SAME addr with no
	// pre-bound net listener: phase7b's net.Listen fails with EADDRINUSE, after
	// HTTP (phase7) is already serving. The bind-failure path must drain HTTP
	// (drainHTTPOnGRPCStartFailure) and surface the error.
	occupied := newLocalListener(t)
	defer func() { _ = occupied.Close() }()

	srv := buildAdapterServer(t, healthPublic, registerHealth)
	asm := minimalGRPCAssembly(t, "grpc-bindfail")
	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srv, occupied.Addr().String()), // no WithGRPCListenerNet → net.Listen on occupied addr
		WithShutdownTimeout(testtime.D2s),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()

	select {
	case err := <-done:
		require.Error(t, err, "gRPC bind failure must abort startup")
		assert.ErrorContains(t, err, "grpc listen", "error must identify the gRPC bind failure")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return on gRPC bind failure")
	}
}

// --- Case 7: phase0 gRPC listener config validation ------------------------

func TestWithGRPCListener_Phase0ConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "negative_shutdown_grace",
			opts: []Option{WithGRPCListener(cell.PrimaryListener, stubGRPCServer{}, ":0", WithGRPCListenerShutdownGrace(-testtime.D2s))},
			want: "negative shutdownGrace",
		},
		{
			name: "empty_addr_no_net",
			opts: []Option{WithGRPCListener(cell.PrimaryListener, stubGRPCServer{}, "")},
			want: "non-empty addr or a pre-bound listener",
		},
		{
			// F4: a zero ListenerRef can never be targeted by GRPCServiceSpec.Listener,
			// so the cell's services would be silently dropped. Reject at phase0.
			name: "zero_ref",
			opts: []Option{WithGRPCListener(cell.ListenerRef{}, stubGRPCServer{}, ":0")},
			want: "zero gRPC listener ref",
		},
		{
			// F4: two WithGRPCListener calls sharing a ref make spec routing
			// ambiguous; reject at phase0 rather than in phase7b after sockets bind.
			name: "duplicate_ref",
			opts: []Option{
				WithGRPCListener(cell.PrimaryListener, stubGRPCServer{}, ":0"),
				WithGRPCListener(cell.PrimaryListener, stubGRPCServer{}, ":0"),
			},
			want: "duplicate WithGRPCListener call for ref",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(clock.Real(), tc.opts...)
			err := b.Run(context.Background())
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
