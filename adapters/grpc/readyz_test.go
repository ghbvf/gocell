package grpc_test

// readyz_test.go — grpc_ready probe + required Registrar (Option 3 #1152).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	grpcadapter "github.com/ghbvf/gocell/adapters/grpc"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
)

// TestNew_RequiresRegistrar asserts New rejects a Config without a Registrar:
// the registrar is the shared method→cellID source the interceptor chain reads,
// so a missing one is a wiring bug (no self-construct fallback, fail-closed).
func TestNew_RequiresRegistrar(t *testing.T) {
	_, err := grpcadapter.New(grpcadapter.Config{
		Addr: ":0",
		TLS:  grpcadapter.TLSConfig{AllowInsecure: true},
		// Registrar omitted → required-dep error.
	})
	require.Error(t, err, "New must reject a Config without a Registrar")
}

// TestServer_Probes_ReadyShape asserts the server exposes exactly one readiness
// probe named grpc_ready that reports unhealthy before serving (so /readyz gates
// the instance out until the gRPC server is actually accepting RPCs).
func TestServer_Probes_ReadyShape(t *testing.T) {
	srv, err := grpcadapter.New(grpcadapter.Config{
		Addr:      ":0",
		TLS:       grpcadapter.TLSConfig{AllowInsecure: true},
		Registrar: runtimegrpc.NewServiceRegistrar(),
	})
	require.NoError(t, err)

	probes := srv.Probes()
	require.Len(t, probes, 1, "gRPC server exposes exactly one readiness probe")
	require.Equal(t, grpcadapter.ProbeReady, probes[0].Name())
	require.Error(t, probes[0].Check(context.Background()),
		"grpc_ready must report unhealthy before the server is serving")
}
