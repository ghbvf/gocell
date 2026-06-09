package grpc_test

// registrar_bindserver_test.go — two-phase registrar guards (Option 3 #1152).
//
// NewServiceRegistrar() builds the method→cellID map with no delegation target
// so the composition root can hand reg.CellIDForMethod to the interceptor chain
// (built before the gRPC server exists) and bind the server afterwards via
// BindServer. Register before BindServer is a wiring bug and must fail-fast.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
)

// TestServiceRegistrar_RegisterBeforeBindServer_Panics asserts Register before
// BindServer fails fast (no silent nil-delegation).
func TestServiceRegistrar_RegisterBeforeBindServer_Panics(t *testing.T) {
	reg := runtimegrpc.NewServiceRegistrar()
	defer func() {
		if recover() == nil {
			t.Fatalf("Register before BindServer must panic (delegation target unbound)")
		}
	}()
	_ = reg.Register(synthSpec("grpc.health.v1", "cell-x", func(r grpc.ServiceRegistrar) {}))
}

// TestServiceRegistrar_CellIDForMethod_BeforeBind is empty until services are
// registered: the map exists from construction, so CellIDForMethod is callable
// before BindServer (it just returns no match).
func TestServiceRegistrar_CellIDForMethod_BeforeBind(t *testing.T) {
	reg := runtimegrpc.NewServiceRegistrar()
	_, ok := reg.CellIDForMethod("/pkg.Svc/Do")
	require.False(t, ok, "no method registered yet")
}

// TestServiceRegistrar_BindServerTwice_Panics asserts the delegation target is
// bound exactly once: a second BindServer is a wiring bug (the adapter binds it
// in New), fail-fast rather than silently swapping the server out from under a
// populated attribution map.
func TestServiceRegistrar_BindServerTwice_Panics(t *testing.T) {
	reg := runtimegrpc.NewServiceRegistrar()
	reg.BindServer(grpc.NewServer())
	defer func() {
		if recover() == nil {
			t.Fatalf("second BindServer must panic (delegation target already bound)")
		}
	}()
	reg.BindServer(grpc.NewServer())
}
