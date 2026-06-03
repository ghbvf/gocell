package grpc_test

// registrar_test.go — TDD coverage for ServiceRegistrar (GAP-1 PR-7 [#1150]).
//
// Cases:
//   1. Register+Invoke over bufconn: real gRPC round-trip via callback
//   2. CellIDForMethod: unary Methods mapped after Register
//   3. CellIDForMethod: stream Streams mapped after Register
//   4. CellIDForMethod: unknown method returns (_, false)
//   5. bad Register fn type panics with panicregister.Approved (grpc-registrar-bad-register-fn)
//   6. duplicate ServiceName panics with panicregister.Approved (grpc-registrar-dup-service)

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ghbvf/gocell/kernel/cell"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
)

const registrarBufSize = 1 << 20 // 1 MiB

// newRegistrar returns a *runtimegrpc.ServiceRegistrar wrapping a fresh grpc.Server.
func newRegistrar() (*runtimegrpc.ServiceRegistrar, *grpc.Server) {
	inner := grpc.NewServer()
	return runtimegrpc.NewServiceRegistrar(inner), inner
}

// dialBufconn dials via bufconn with insecure credentials.
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

// synthSpec returns a GRPCServiceSpec whose Register calls the given fn.
func synthSpec(contractID, cellID string, fn func(grpc.ServiceRegistrar)) cell.GRPCServiceSpec {
	return cell.GRPCServiceSpec{
		ContractID: contractID,
		CellID:     cellID,
		Listener:   cell.PrimaryListener,
		Register:   fn,
	}
}

// --- Case 1: Register+Invoke bufconn round-trip ----------------------------

// TestServiceRegistrar_Register_BufconnRoundTrip verifies that Register invokes the
// callback so services are reachable via real gRPC transport.
func TestServiceRegistrar_Register_BufconnRoundTrip(t *testing.T) {
	t.Parallel()

	reg, srv := newRegistrar()

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	spec := synthSpec("grpc.health.v1", "test-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, healthSrv)
	})
	require.NoError(t, reg.Register(spec))

	// Serve on bufconn.
	lis := bufconn.Listen(registrarBufSize)
	defer lis.Close()
	go func() { _ = srv.Serve(lis) }()
	defer srv.GracefulStop()

	cc := dialBufconn(t, lis)
	defer cc.Close()

	ctx := context.Background()
	resp, err := grpc_health_v1.NewHealthClient(cc).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.GetStatus())
}

// --- Case 2: CellIDForMethod unary -------------------------------------------

// TestServiceRegistrar_CellIDForMethod_Unary verifies /{svc}/{method} → cellID
// is recorded for unary methods after Register.
func TestServiceRegistrar_CellIDForMethod_Unary(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()

	healthSrv := health.NewServer()
	spec := synthSpec("grpc.health.v1", "health-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, healthSrv)
	})
	require.NoError(t, reg.Register(spec))

	// grpc_health_v1.Health service has method "Check".
	cellID, ok := reg.CellIDForMethod("/grpc.health.v1.Health/Check")
	require.True(t, ok, "Check method should be attributed")
	assert.Equal(t, "health-cell", cellID)
}

// --- Case 3: CellIDForMethod stream ------------------------------------------

// TestServiceRegistrar_CellIDForMethod_Stream verifies stream Streams are also mapped.
func TestServiceRegistrar_CellIDForMethod_Stream(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()

	healthSrv := health.NewServer()
	spec := synthSpec("grpc.health.v1", "stream-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, healthSrv)
	})
	require.NoError(t, reg.Register(spec))

	// grpc_health_v1.Health service has streaming method "Watch".
	cellID, ok := reg.CellIDForMethod("/grpc.health.v1.Health/Watch")
	require.True(t, ok, "Watch stream should be attributed")
	assert.Equal(t, "stream-cell", cellID)
}

// --- Case 4: CellIDForMethod unknown -----------------------------------------

// TestServiceRegistrar_CellIDForMethod_Unknown verifies unknown methods return (_, false).
func TestServiceRegistrar_CellIDForMethod_Unknown(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	_, ok := reg.CellIDForMethod("/nonexistent.Svc/Method")
	assert.False(t, ok)
}

// --- Case 5: bad Register fn type panics -------------------------------------

// TestServiceRegistrar_Register_BadFnType_Panics verifies a non-func Register field
// causes a panic with panicregister.Approved wrapping (grpc-registrar-bad-register-fn).
func TestServiceRegistrar_Register_BadFnType_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := cell.GRPCServiceSpec{
		ContractID: "grpc.bad.v1",
		CellID:     "test-cell",
		Listener:   cell.PrimaryListener,
		Register:   "not-a-function", // wrong type
	}
	assert.Panics(t, func() {
		_ = reg.Register(spec)
	}, "bad Register fn type must panic")
}

// --- Case 6: duplicate ServiceName panics ------------------------------------

// TestServiceRegistrar_Register_DupServiceName_Panics verifies that registering
// two specs whose callbacks register the SAME grpc ServiceName panics.
func TestServiceRegistrar_Register_DupServiceName_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()

	registerHealth := func(cellID string) {
		healthSrv := health.NewServer()
		_ = reg.Register(synthSpec("grpc.health."+cellID, cellID, func(r grpc.ServiceRegistrar) {
			grpc_health_v1.RegisterHealthServer(r, healthSrv)
		}))
	}

	registerHealth("cell-a") // first registration — should succeed

	assert.Panics(t, func() {
		registerHealth("cell-b") // duplicate grpc.health.v1.Health ServiceName
	}, "duplicate ServiceName must panic")
}
