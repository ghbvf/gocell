package bootstrap

// grpc_drain_test.go — TDD coverage for the gRPC service drain in phase7b
// (GAP-1 PR-7 [#1150]).
//
// Cases:
//   1. happy path: cell's GRPCServices are drained and registered before Serve
//   2. undeclared listener ref → fail-fast
//   3. duplicate gRPC listener ref → fail-fast
//   4. CellID mismatch → fail-fast
//   5. registration precedes Serve — spy GRPCServiceRegistrar records ordering
//   6. multi-listener routing: two cells, two refs, each service only on its listener
//   7. F2 fix: cell declares GRPCServices + zero WithGRPCListener → fail-fast

import (
	"context"
	"net"
	"sync/atomic"
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
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// GRPCServiceRegistrar is the bootstrap-local narrow interface (defined in grpc_listener.go).
// We reference it here to verify the fake satisfies it.
var _ GRPCServiceRegistrar = (*runtimegrpc.ServiceRegistrar)(nil)

// buildDrainAdapterServer builds an adapters/grpc.Server with no auth (all public),
// for use in drain tests that don't care about auth.
func buildDrainAdapterServer(t *testing.T) *adaptersgrpc.Server {
	t.Helper()
	reg := runtimegrpc.NewServiceRegistrar()
	drain := runtimegrpc.NewDrainSignal()
	deps := interceptor.Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        &bootstrapTestVerifier{},
		AuthOptions:     []interceptor.AuthOption{interceptor.WithPublicMethod(func(string) bool { return true })},
		Registrar:       reg, // Option 3: shared registrar (#1152)
		CellIDClosedSet: []string{"bootstrap-test-cell"},
		Drain:           drain,
	}
	srv, err := adaptersgrpc.New(adaptersgrpc.Config{
		Addr:         ":0",
		TLS:          adaptersgrpc.TLSConfig{AllowInsecure: true},
		Interceptors: interceptor.NewServerInterceptors(deps),
	})
	require.NoError(t, err)
	return srv
}

// grpcBufDial dials a bufconn listener with insecure credentials.
func grpcBufDial(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
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

// newGRPCDrainCell constructs a grpcDrainCell with a properly initialized BaseCell.
func newGRPCDrainCell(t *testing.T, id string, ref cell.ListenerRef, registerFn func(grpc.ServiceRegistrar)) *grpcDrainCell {
	t.Helper()
	base := cell.MustNewBaseCell(&metadata.CellMeta{ID: id})
	return &grpcDrainCell{BaseCell: base, id: id, ref: ref, registerFn: registerFn}
}

// drainTestAssembly builds a CoreAssembly containing one stub cell that
// registers a GRPCServiceSpec targeting the given listener ref.
func drainTestAssembly(t *testing.T, id string, ref cell.ListenerRef, registerFn func(grpc.ServiceRegistrar)) *assembly.CoreAssembly {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{ID: id, DurabilityMode: outbox.DurabilityDemo})
	c := newGRPCDrainCell(t, id, ref, registerFn)
	require.NoError(t, asm.Register(c))
	return asm
}

// --- Case 1: happy path — drain + register before Serve ----------------------

func TestGRPCDrain_HappyPath(t *testing.T) {
	const cellID = "grpc-drain-happy"
	lis := bufconn.Listen(grpcTestBufSize)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	asm := drainTestAssembly(t, cellID, cell.PrimaryListener, func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, healthSrv)
	})

	srv := buildDrainAdapterServer(t)
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

	cc := grpcBufDial(t, lis)
	defer func() { _ = cc.Close() }()

	// Wait for health to be SERVING (proves service was registered before Serve).
	testwait.External(t, "grpc-drain-serving", func() bool {
		rctx, rcancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer rcancel()
		resp, err := grpc_health_v1.NewHealthClient(cc).Check(rctx, &grpc_health_v1.HealthCheckRequest{})
		return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
	}, testtime.EventuallyDefault, testtime.MediumPoll, "gRPC health must be SERVING after drain")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return after cancel")
	}
}

// --- Case 2: undeclared listener ref → fail-fast ----------------------------

func TestGRPCDrain_UndeclaredRef_FailFast(t *testing.T) {
	const cellID = "grpc-drain-undeclared"
	// Cell declares PrimaryListener but bootstrap only declares a different ref.
	asm := drainTestAssembly(t, cellID, cell.PrimaryListener, func(r grpc.ServiceRegistrar) {})

	srv := buildDrainAdapterServer(t)
	// WithGRPCListener with InternalListener ref — PrimaryListener is undeclared.
	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.InternalListener, srv, ":0"),
		WithShutdownTimeout(testtime.D2s),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorContains(t, err, "undeclared")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not fail-fast on undeclared ref")
	}
}

// --- Case 3: duplicate gRPC listener ref → fail-fast ------------------------

func TestGRPCDrain_DuplicateRef_FailFast(t *testing.T) {
	asm := assembly.New(clock.Real(), assembly.Config{ID: "grpc-dup-ref", DurabilityMode: outbox.DurabilityDemo})
	srvA := buildDrainAdapterServer(t)
	srvB := buildDrainAdapterServer(t)

	// Two listeners registered with the same ref.
	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srvA, ":0"),
		WithGRPCListener(cell.PrimaryListener, srvB, ":0"),
		WithShutdownTimeout(testtime.D2s),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorContains(t, err, "duplicate")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not fail-fast on duplicate ref")
	}
}

// --- Case 4: CellID mismatch → fail-fast ------------------------------------

func TestGRPCDrain_CellIDMismatch_FailFast(t *testing.T) {
	const cellID = "grpc-cellid-mismatch"
	lis := bufconn.Listen(grpcTestBufSize)
	// The cell registers a spec with a different CellID than the snapshot key.
	base := cell.MustNewBaseCell(&metadata.CellMeta{ID: cellID})
	c := &grpcDrainCell{BaseCell: base, id: cellID, ref: cell.PrimaryListener, overrideCellID: "wrong-cell"}
	asm2 := assembly.New(clock.Real(), assembly.Config{ID: cellID + "2", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm2.Register(c))

	srv := buildDrainAdapterServer(t)
	b := New(
		clock.Real(),
		WithAssembly(asm2),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srv, ":0", WithGRPCListenerNet(lis)),
		WithShutdownTimeout(testtime.D2s),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorContains(t, err, "CellID")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not fail-fast on CellID mismatch")
	}
}

// ---------------------------------------------------------------------------
// grpcDrainCell — minimal Cell implementation for drain tests.
// ---------------------------------------------------------------------------

type grpcDrainCell struct {
	*cell.BaseCell
	id             string
	ref            cell.ListenerRef
	registerFn     func(grpc.ServiceRegistrar)
	overrideCellID string // non-empty → use this CellID in the spec (mismatch test)
}

func (c *grpcDrainCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	cellID := c.id
	if c.overrideCellID != "" {
		cellID = c.overrideCellID
	}
	spec := cell.GRPCServiceSpec{
		ContractID: "grpc." + c.id + ".v1",
		CellID:     cellID,
		Listener:   c.ref,
		Register:   c.registerFn,
	}
	return reg.GRPCService(spec)
}

// ---------------------------------------------------------------------------
// Case 5: registration precedes Serve — spy GRPCServiceRegistrar
// ---------------------------------------------------------------------------

// spyGRPCServer is a GRPCServer that records whether Register was called before
// the first Serve call. It wraps a real adapters/grpc.Server so the spy can
// observe ordering while the real server handles actual gRPC traffic.
type spyGRPCServer struct {
	// inner is the real server; calls are always forwarded.
	inner GRPCServer
	// registeredBeforeServe is set to 1 atomically the moment Register is invoked.
	registeredBeforeServe atomic.Int32
	// registeredAtServe captures registeredBeforeServe's value at the exact
	// instant Serve begins. The assertion reads THIS snapshot — not the live
	// registeredBeforeServe flag — to avoid a false positive: a regressed
	// register-after-Serve implementation could still flip the live flag to 1
	// between the test observing serveStarted and reading the flag. The snapshot
	// pins the value as-of Serve entry.
	registeredAtServe atomic.Int32
	// serveStarted is closed when Serve is first called.
	serveStarted chan struct{}
	// serveOnce guards serveStarted close.
	serveOnce atomic.Int32
}

func (s *spyGRPCServer) Registrar() GRPCServiceRegistrar {
	return &spyGRPCServiceRegistrar{inner: s.inner.Registrar(), spy: s}
}

func (s *spyGRPCServer) Serve(ctx context.Context, lis net.Listener) error {
	if s.serveOnce.CompareAndSwap(0, 1) {
		// Snapshot the registration flag BEFORE signaling serveStarted, so the
		// asserted value is fixed at Serve entry and cannot be tainted by a later
		// (regressed) registration.
		s.registeredAtServe.Store(s.registeredBeforeServe.Load())
		close(s.serveStarted)
	}
	return s.inner.Serve(ctx, lis)
}

func (s *spyGRPCServer) Close(ctx context.Context) error {
	return s.inner.Close(ctx)
}

func (s *spyGRPCServer) Probes() []healthz.Probe { return s.inner.Probes() }

// spyGRPCServiceRegistrar records that Register was called, then forwards.
type spyGRPCServiceRegistrar struct {
	inner GRPCServiceRegistrar
	spy   *spyGRPCServer
}

func (r *spyGRPCServiceRegistrar) Register(spec cell.GRPCServiceSpec) error {
	r.spy.registeredBeforeServe.Store(1)
	return r.inner.Register(spec)
}

// TestGRPCDrain_RegistrationPrecedesServe verifies that bootstrap calls
// Register on each service BEFORE the gRPC server's Serve goroutine starts,
// so services are reachable from the very first accepted connection (no race).
//
// This test pins the key safety invariant: grpc-go fatals if RegisterService
// is called after Serve has started; the drain ordering (phase7b: register →
// grpcServeAll) must hold.
func TestGRPCDrain_RegistrationPrecedesServe(t *testing.T) {
	const cellID = "grpc-order-spy"
	lis := bufconn.Listen(grpcTestBufSize)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	realSrv := buildDrainAdapterServer(t)
	spy := &spyGRPCServer{inner: realSrv, serveStarted: make(chan struct{})}

	asm := drainTestAssembly(t, cellID, cell.PrimaryListener, func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, healthSrv)
	})

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, spy, ":0", WithGRPCListenerNet(lis)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for Serve to start (so the ordering window is observable).
	select {
	case <-spy.serveStarted:
	case <-time.After(testtime.D5s):
		t.Fatal("spy Serve was never called")
	}

	// Assert: Register must have been called BEFORE Serve. Read the snapshot
	// captured at Serve entry (not the live flag) so a register-after-Serve
	// regression cannot pass by flipping the flag after serveStarted fires.
	assert.Equal(t, int32(1), spy.registeredAtServe.Load(),
		"Register must be called before Serve starts (snapshot at Serve entry)")

	// Also verify the service is actually reachable (defense-in-depth).
	cc := grpcBufDial(t, lis)
	defer func() { _ = cc.Close() }()
	testwait.External(t, "grpc-spy-serving", func() bool {
		rctx, rcancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer rcancel()
		resp, err := grpc_health_v1.NewHealthClient(cc).Check(rctx, &grpc_health_v1.HealthCheckRequest{})
		return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
	}, testtime.EventuallyDefault, testtime.MediumPoll, "gRPC health must be SERVING after drain")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return after cancel")
	}
}

// ---------------------------------------------------------------------------
// Case 6: multi-listener routing
// ---------------------------------------------------------------------------

// TestGRPCDrain_MultiListenerRouting verifies that when two cells each declare
// a GRPCServiceSpec on a DIFFERENT ListenerRef, each service is reachable only
// on its declared listener (correct routing) and unreachable on the other.
func TestGRPCDrain_MultiListenerRouting(t *testing.T) {
	const (
		cellA = "grpc-multi-a"
		cellB = "grpc-multi-b"
	)
	lisA := bufconn.Listen(grpcTestBufSize)
	lisB := bufconn.Listen(grpcTestBufSize)

	// Cell A registers the gRPC Health service on PrimaryListener.
	healthSrvA := health.NewServer()
	healthSrvA.SetServingStatus("cell-a", grpc_health_v1.HealthCheckResponse_SERVING)

	// Cell B registers a synthetic no-op service on InternalListener.
	// We use a different grpc.ServiceDesc ServiceName to distinguish the two.
	const bServiceName = "test.CellBService"
	bDesc := &grpc.ServiceDesc{
		ServiceName: bServiceName,
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Ping",
			Handler: func(_ any, ctx context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
			},
		}},
	}

	// Assembly with two cells, each targeting a different listener.
	asm := assembly.New(clock.Real(), assembly.Config{ID: cellA + cellB, DurabilityMode: outbox.DurabilityDemo})
	cA := newGRPCDrainCell(t, cellA, cell.PrimaryListener, func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, healthSrvA)
	})
	cB := newGRPCDrainCell(t, cellB, cell.InternalListener, func(r grpc.ServiceRegistrar) {
		r.RegisterService(bDesc, struct{}{})
	})
	require.NoError(t, asm.Register(cA))
	require.NoError(t, asm.Register(cB))

	srvA := buildDrainAdapterServer(t)
	srvB := buildDrainAdapterServer(t)

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		WithGRPCListener(cell.PrimaryListener, srvA, ":0", WithGRPCListenerNet(lisA)),
		WithGRPCListener(cell.InternalListener, srvB, ":0", WithGRPCListenerNet(lisB)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	ccA := grpcBufDial(t, lisA)
	defer func() { _ = ccA.Close() }()
	ccB := grpcBufDial(t, lisB)
	defer func() { _ = ccB.Close() }()

	// Cell A's Health service is reachable on lisA.
	testwait.External(t, "grpc-multi-a-serving", func() bool {
		rctx, rcancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer rcancel()
		resp, err := grpc_health_v1.NewHealthClient(ccA).Check(rctx,
			&grpc_health_v1.HealthCheckRequest{Service: "cell-a"})
		return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
	}, testtime.EventuallyDefault, testtime.MediumPoll, "cell-a health must be SERVING on lisA")

	// Cell B's Ping service is reachable on lisB.
	testwait.External(t, "grpc-multi-b-serving", func() bool {
		rctx, rcancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer rcancel()
		var resp grpc_health_v1.HealthCheckResponse
		err := ccB.Invoke(rctx, "/"+bServiceName+"/Ping",
			&grpc_health_v1.HealthCheckRequest{}, &resp)
		return err == nil
	}, testtime.EventuallyDefault, testtime.MediumPoll, "cell-b Ping must be reachable on lisB")

	// Cell A's Health service is NOT registered on lisB (different listener).
	rctx, rcancel := context.WithTimeout(context.Background(), testtime.D500ms)
	defer rcancel()
	_, errHealthOnB := grpc_health_v1.NewHealthClient(ccB).Check(rctx,
		&grpc_health_v1.HealthCheckRequest{Service: "cell-a"})
	// grpc-go returns "unknown service grpc.health.v1.Health" when the service
	// is not registered; we expect a non-nil error here.
	assert.Error(t, errHealthOnB, "cell-a Health service must NOT be reachable on cell-b's listener")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not return after cancel")
	}
}

// ---------------------------------------------------------------------------
// Case 7: F2 fix — GRPCServices declared but no WithGRPCListener → fail-fast
// ---------------------------------------------------------------------------

// TestGRPCDrain_OrphanServices_FailFast verifies that when a cell declares
// GRPCServiceSpec(s) but the composition root adds no WithGRPCListener, Run
// returns a descriptive error instead of silently dropping the services.
func TestGRPCDrain_OrphanServices_FailFast(t *testing.T) {
	const cellID = "grpc-orphan"
	// Cell declares a GRPCServiceSpec; bootstrap has no gRPC listener at all.
	asm := drainTestAssembly(t, cellID, cell.PrimaryListener, func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})

	b := New(
		clock.Real(),
		WithAssembly(asm),
		healthListenerOpt(t),
		// Intentionally NO WithGRPCListener.
		WithShutdownTimeout(testtime.D2s),
	)

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err, "orphan gRPC services must cause a fail-fast error")
		assert.ErrorContains(t, err, "gRPC service",
			"error must identify the orphan gRPC service situation")
		assert.ErrorContains(t, err, cellID,
			"error must name the cell with orphan services")
	case <-time.After(testtime.D5s):
		t.Fatal("Run did not fail-fast on orphan gRPC services")
	}
}
