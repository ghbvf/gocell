package grpc_test

// readyz_test.go — grpc_ready probe + required Registrar (Option 3 #1152).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

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
		Interceptors: runtimegrpc.NewServerInterceptorsBundle(
			[]grpc.ServerOption{grpc.EmptyServerOption{}},
			nil,
			runtimegrpc.NewDrainSignal(),
		),
		// Registrar omitted → required-dep error.
	})
	require.Error(t, err, "New must reject a Config without a Registrar")
}

func readyzBundle(drain *runtimegrpc.DrainSignal) runtimegrpc.ServerInterceptors {
	return runtimegrpc.NewServerInterceptorsBundle(
		[]grpc.ServerOption{grpc.EmptyServerOption{}},
		runtimegrpc.NewServiceRegistrar(),
		drain,
	)
}

// TestNew_RequiresDrain asserts New rejects a Config without a Drain signal: the
// drain is the shared signal the stream interceptor chain binds in-flight streams
// to (PR-10 #1153), so a missing one is a wiring bug (no `if drain != nil`
// fallback, fail-closed — same discipline as the required Registrar).
func TestNew_RequiresDrain(t *testing.T) {
	_, err := grpcadapter.New(grpcadapter.Config{
		Addr:         ":0",
		TLS:          grpcadapter.TLSConfig{AllowInsecure: true},
		Interceptors: readyzBundle(nil),
		// Drain omitted → required-dep error.
	})
	require.Error(t, err, "New must reject a Config without a Drain signal")
}

// TestNew_RejectsZeroValueDrain asserts New rejects a non-nil but zero-value
// DrainSignal (new(grpcadapter)... is impossible — the type is in runtimegrpc):
// new(runtimegrpc.DrainSignal) passes a bare `== nil` check but has a nil cancel
// and would panic at GracefulStop, so Config.validate calls Drain.Validate().
func TestNew_RejectsZeroValueDrain(t *testing.T) {
	_, err := grpcadapter.New(grpcadapter.Config{
		Addr:         ":0",
		TLS:          grpcadapter.TLSConfig{AllowInsecure: true},
		Interceptors: readyzBundle(new(runtimegrpc.DrainSignal)), // non-nil zero-value → invalid
	})
	require.Error(t, err, "New must reject a zero-value DrainSignal (would panic at GracefulStop)")
}

// TestServer_Probes_ReadyShape asserts the server exposes exactly one readiness
// probe named grpc_ready that reports unhealthy before serving (so /readyz gates
// the instance out until the gRPC server is actually accepting RPCs).
func TestServer_Probes_ReadyShape(t *testing.T) {
	srv, err := grpcadapter.New(grpcadapter.Config{
		Addr:         ":0",
		TLS:          grpcadapter.TLSConfig{AllowInsecure: true},
		Interceptors: readyzBundle(runtimegrpc.NewDrainSignal()),
	})
	require.NoError(t, err)

	probes := srv.Probes()
	require.Len(t, probes, 1, "gRPC server exposes exactly one readiness probe")
	require.Equal(t, grpcadapter.ProbeReady, probes[0].Name())
	require.Error(t, probes[0].Check(context.Background()),
		"grpc_ready must report unhealthy before the server is serving")
}
