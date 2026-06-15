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
//   7. duplicate ServiceName panic names both first/current owner (F3)
//   8. typed-nil Register callback panics (grpc-registrar-nil-register-fn) (F2)
//   9. no-op callback (0 services) panics (grpc-registrar-service-count) (F1)
//  10. multi-register callback (>1 service) panics (grpc-registrar-service-count) (F1)
//  11. registrar retained past callback (escaped scope) panics (grpc-registrar-escaped-scope) (F1)

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	runtimegrpc "github.com/ghbvf/gocell/framework/runtime/grpc"
)

const registrarBufSize = 1 << 20 // 1 MiB

// newRegistrar returns a *runtimegrpc.ServiceRegistrar bound to a fresh
// grpc.Server. The registrar is created without an inner server (two-phase,
// Option 3 #1152) and the delegation target is bound before any Register call.
func newRegistrar() (*runtimegrpc.ServiceRegistrar, *grpc.Server) {
	inner := grpc.NewServer()
	reg := runtimegrpc.NewServiceRegistrar()
	reg.BindServer(inner)
	return reg, inner
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
	defer func() { _ = lis.Close() }()
	go func() { _ = srv.Serve(lis) }()
	defer srv.GracefulStop()

	cc := dialBufconn(t, lis)
	defer func() { _ = cc.Close() }()

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

// --- IsPublicMethod (#1675): per-method public auth overlay ------------------

// TestServiceRegistrar_IsPublicMethod verifies that GRPCServiceSpec.PublicMethods
// (#1675) are aggregated into the registrar's public-method set after Register,
// and that undeclared / unknown methods are authed (fail-closed default false).
func TestServiceRegistrar_IsPublicMethod(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := synthSpec("grpc.health.v1", "test-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})
	spec.PublicMethods = []string{"/grpc.health.v1.Health/Check"}
	require.NoError(t, reg.Register(spec))

	assert.True(t, reg.IsPublicMethod("/grpc.health.v1.Health/Check"),
		"a method declared in spec.PublicMethods must be public")
	assert.False(t, reg.IsPublicMethod("/grpc.health.v1.Health/Watch"),
		"a registered-but-undeclared method must be authed (fail-closed)")
	assert.False(t, reg.IsPublicMethod("/nonexistent.Svc/Method"),
		"an unknown method must be authed (fail-closed)")
}

// TestServiceRegistrar_IsPublicMethod_EmptyDefault verifies a registrar whose
// specs declare no PublicMethods treats every method as authed (fail-closed).
func TestServiceRegistrar_IsPublicMethod_EmptyDefault(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := synthSpec("grpc.health.v1", "test-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})
	require.NoError(t, reg.Register(spec))

	assert.False(t, reg.IsPublicMethod("/grpc.health.v1.Health/Check"),
		"no PublicMethods declared → every method authed (fail-closed)")
}

// TestServiceRegistrar_IsPublicMethod_MultiSpecAggregation verifies PublicMethods
// are aggregated ACROSS multiple registered specs (each cell contributes its own
// public set to the one shared registrar the auth interceptor consults).
func TestServiceRegistrar_IsPublicMethod_MultiSpecAggregation(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()

	specA := synthSpec("grpc.health.a.v1", "cell-a", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})
	specA.PublicMethods = []string{"/grpc.health.v1.Health/Check"}
	require.NoError(t, reg.Register(specA))

	// A second spec registering a DIFFERENT service contributes its own public set;
	// service-name dedup forbids re-registering grpc.health.v1.Health, so use a
	// distinct ServiceDesc.
	specB := cell.GRPCServiceSpec{
		ContractID:    "grpc.spy.b.v1",
		CellID:        "cell-b",
		Listener:      cell.PrimaryListener,
		PublicMethods: []string{"/spy.v1.Spy/Ping"},
		Register: func(r grpc.ServiceRegistrar) {
			r.RegisterService(&grpc.ServiceDesc{
				ServiceName: "spy.v1.Spy",
				HandlerType: (*any)(nil),
				Methods:     []grpc.MethodDesc{{MethodName: "Ping"}},
			}, struct{}{})
		},
	}
	require.NoError(t, reg.Register(specB))

	assert.True(t, reg.IsPublicMethod("/grpc.health.v1.Health/Check"), "specA's public method must aggregate")
	assert.True(t, reg.IsPublicMethod("/spy.v1.Spy/Ping"), "specB's public method must aggregate")
	assert.False(t, reg.IsPublicMethod("/spy.v1.Spy/Other"), "an undeclared method stays authed (fail-closed)")
}

// --- #2008: per-method permission overlay ------------------------------------

// TestServiceRegistrar_PermissionForMethod verifies a method declared in
// spec.MethodPermissions resolves to its sealed authz.Permission, and an
// undeclared method reports ok=false (the fail-closed default the PDP gate denies).
func TestServiceRegistrar_PermissionForMethod(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := synthSpec("grpc.health.v1", "test-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})
	spec.MethodPermissions = map[string]string{
		"/grpc.health.v1.Health/Watch": authz.PermDeviceCommand().String(),
	}
	require.NoError(t, reg.Register(spec))

	perm, ok := reg.PermissionForMethod("/grpc.health.v1.Health/Watch")
	require.True(t, ok, "a method declared in spec.MethodPermissions must resolve")
	assert.Equal(t, authz.PermDeviceCommand(), perm,
		"the resolved value must be the sealed registry singleton")

	_, ok = reg.PermissionForMethod("/grpc.health.v1.Health/Check")
	assert.False(t, ok, "an undeclared method has no permission mapping (fail-closed → gate denies)")
	_, ok = reg.PermissionForMethod("/nonexistent.Svc/Method")
	assert.False(t, ok, "an unknown method has no permission mapping (fail-closed)")
}

// TestServiceRegistrar_PermissionForMethod_EmptyDefault verifies a registrar whose
// specs declare no MethodPermissions reports ok=false for every method (the PDP
// gate then denies — strict fail-closed).
func TestServiceRegistrar_PermissionForMethod_EmptyDefault(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	require.NoError(t, reg.Register(synthSpec("grpc.health.v1", "test-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})))

	_, ok := reg.PermissionForMethod("/grpc.health.v1.Health/Check")
	assert.False(t, ok, "no MethodPermissions declared → no mapping (fail-closed)")
}

// TestServiceRegistrar_Register_UnknownPermission_Panics verifies a
// MethodPermissions value that is NOT a member of the closed authz registry fails
// fast at registration (grpc-registrar-unknown-permission). A string surviving to
// runtime is a wiring bug (hand-written, bypassing the contractgen + FMT-41
// build-time guards); deny startup rather than gate on a forged action.
func TestServiceRegistrar_Register_UnknownPermission_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := synthSpec("grpc.health.v1", "test-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})
	spec.MethodPermissions = map[string]string{
		"/grpc.health.v1.Health/Watch": "not:a-registered-permission",
	}
	assert.Panics(t, func() {
		_ = reg.Register(spec)
	}, "an unknown permission action string must fail fast at registration")
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

// --- Case 7: duplicate ServiceName panic names both owners (F3) ---------------

// TestServiceRegistrar_Register_DupServiceName_OwnerContext verifies the dup
// panic message identifies BOTH the first owner (cell + contractID) and the
// re-registering owner, so an operator can pinpoint the colliding cells without
// guessing.
func TestServiceRegistrar_Register_DupServiceName_OwnerContext(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()

	require.NoError(t, reg.Register(synthSpec("grpc.health.first.v1", "cell-first", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})))

	defer func() {
		rec := recover()
		require.NotNil(t, rec, "duplicate ServiceName must panic")
		msg := fmt.Sprint(rec)
		assert.Contains(t, msg, "cell-first", "panic must name the first owner cell")
		assert.Contains(t, msg, "grpc.health.first.v1", "panic must name the first owner contractID")
		assert.Contains(t, msg, "cell-second", "panic must name the re-registering cell")
		assert.Contains(t, msg, "grpc.health.second.v1", "panic must name the re-registering contractID")
	}()

	_ = reg.Register(synthSpec("grpc.health.second.v1", "cell-second", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	}))
}

// --- Case 8: typed-nil Register callback panics (F2) -------------------------

// TestServiceRegistrar_Register_TypedNilCallback_Panics verifies a typed-nil
// func(grpc.ServiceRegistrar) boxed in any — which slips past kernel's bare-nil
// Validate — fails fast through the Approved funnel instead of an unregistered
// Go runtime nil-func panic.
func TestServiceRegistrar_Register_TypedNilCallback_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	var nilFn func(grpc.ServiceRegistrar) // typed nil
	spec := cell.GRPCServiceSpec{
		ContractID: "grpc.typednil.v1",
		CellID:     "test-cell",
		Listener:   cell.PrimaryListener,
		Register:   nilFn, // boxed: spec.Register != nil (typed), but the func is nil
	}
	// Sanity: the kernel-level bare-nil guard does NOT catch a typed-nil.
	require.NoError(t, spec.Validate(), "kernel Validate only catches bare-nil Register")

	assert.Panics(t, func() {
		_ = reg.Register(spec)
	}, "typed-nil Register callback must panic")
}

// --- Case 9: no-op callback (0 services) panics (F1) -------------------------

// TestServiceRegistrar_Register_NoOpCallback_Panics verifies a callback that
// registers nothing panics: a declared spec that serves no service is a bug.
func TestServiceRegistrar_Register_NoOpCallback_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := synthSpec("grpc.noop.v1", "noop-cell", func(_ grpc.ServiceRegistrar) {
		// registers nothing
	})
	assert.Panics(t, func() {
		_ = reg.Register(spec)
	}, "no-op callback (0 services) must panic")
}

// --- Case 10: multi-register callback (>1 service) panics (F1) ---------------

// TestServiceRegistrar_Register_MultiRegister_Panics verifies a callback that
// registers more than one service panics: one spec must map to one service.
func TestServiceRegistrar_Register_MultiRegister_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()
	spec := synthSpec("grpc.multi.v1", "multi-cell", func(r grpc.ServiceRegistrar) {
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
		r.RegisterService(&grpc.ServiceDesc{ServiceName: "test.ExtraService"}, struct{}{})
	})
	assert.Panics(t, func() {
		_ = reg.Register(spec)
	}, "callback registering >1 service must panic")
}

// --- Case 11: escaped registrar use after callback panics (F1) ---------------

// TestServiceRegistrar_Register_EscapedScope_Panics verifies that a registrar
// retained by the callback and used after it returns fails fast — closing the
// loophole where a cell could register a service after Serve (which grpc-go
// fatals on).
func TestServiceRegistrar_Register_EscapedScope_Panics(t *testing.T) {
	t.Parallel()

	reg, _ := newRegistrar()

	var captured grpc.ServiceRegistrar
	spec := synthSpec("grpc.escape.v1", "escape-cell", func(r grpc.ServiceRegistrar) {
		captured = r // stash the registrar to use later
		grpc_health_v1.RegisterHealthServer(r, health.NewServer())
	})
	// In-scope registration succeeds (exactly one service).
	require.NoError(t, reg.Register(spec))
	require.NotNil(t, captured)

	// Using the escaped registrar after the scope closed must panic.
	assert.Panics(t, func() {
		captured.RegisterService(&grpc.ServiceDesc{ServiceName: "test.EscapedService"}, struct{}{})
	}, "escaped registrar use after callback must panic")
}
