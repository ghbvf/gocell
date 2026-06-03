package bootstrap

// grpc_drain_test.go — TDD coverage for the gRPC service drain in phase7b
// (GAP-1 PR-7 [#1150]).
//
// Cases:
//   1. happy path: cell's GRPCServices are drained and registered before Serve
//   2. undeclared listener ref → fail-fast
//   3. duplicate gRPC listener ref → fail-fast
//   4. CellID mismatch → fail-fast
//   5. registration precedes Serve (services callable immediately after Run starts)

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
	chain := interceptor.NewUnaryChain(interceptor.Deps{
		Collector:   metrics.NewInMemoryGRPCCollector(),
		Clock:       clock.Real(),
		Verifier:    &bootstrapTestVerifier{},
		AuthOptions: []interceptor.AuthOption{interceptor.WithPublicMethod(func(string) bool { return true })},
	})
	srv, err := adaptersgrpc.New(adaptersgrpc.Config{
		Addr:          ":0",
		TLS:           adaptersgrpc.TLSConfig{AllowInsecure: true},
		ServerOptions: []grpc.ServerOption{chain},
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

// drainTestAssembly builds a CoreAssembly containing one stub cell that
// registers a GRPCServiceSpec targeting the given listener ref.
func drainTestAssembly(t *testing.T, id string, ref cell.ListenerRef, registerFn func(grpc.ServiceRegistrar)) *assembly.CoreAssembly {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{ID: id, DurabilityMode: outbox.DurabilityDemo})
	c := &grpcDrainCell{id: id, ref: ref, registerFn: registerFn}
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
	defer cc.Close()

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
	asm := drainTestAssembly(t, cellID, cell.PrimaryListener, func(r grpc.ServiceRegistrar) {})
	// Patch the cell so its spec has a mismatched CellID.
	c := &grpcDrainCell{id: cellID, ref: cell.PrimaryListener, overrideCellID: "wrong-cell"}
	asm2 := assembly.New(clock.Real(), assembly.Config{ID: cellID + "2", DurabilityMode: outbox.DurabilityDemo})
	// We use a different approach: use a cell that registers the spec with wrong CellID.
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
	cell.BaseCell
	id             string
	ref            cell.ListenerRef
	registerFn     func(grpc.ServiceRegistrar)
	overrideCellID string // non-empty → use this CellID in the spec (mismatch test)
}

func (c *grpcDrainCell) Init(_ context.Context, reg cell.Registrar) error {
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
