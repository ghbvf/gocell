package grpc_test

// readyz_test.go — grpc_ready probe + required Registrar (Option 3 #1152).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	grpcadapter "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/kernel/clock"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
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

func readyzDeps() interceptor.Deps {
	return interceptor.Deps{
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Clock:           clock.Real(),
		Verifier:        adapterTestVerifier{},
		AuthOptions:     []interceptor.AuthOption{interceptor.WithPublicMethod(func(string) bool { return true })},
		Registrar:       runtimegrpc.NewServiceRegistrar(),
		CellIDClosedSet: []string{"_grpc-test", "_integration-test"},
		Drain:           runtimegrpc.NewDrainSignal(),
	}
}

// TestNew_RequiresDrain asserts New rejects a Config without a Drain signal: the
// drain is the shared signal the stream interceptor chain binds in-flight streams
// to (PR-10 #1153), so a missing one is a wiring bug (no `if drain != nil`
// fallback, fail-closed — same discipline as the required Registrar).
func TestNew_RequiresDrain(t *testing.T) {
	deps := readyzDeps()
	deps.Drain = nil
	_, err := grpcadapter.New(grpcadapter.Config{
		Addr:         ":0",
		TLS:          grpcadapter.TLSConfig{AllowInsecure: true},
		Interceptors: deps,
		// Drain omitted → required-dep error.
	})
	require.Error(t, err, "New must reject a Config without a Drain signal")
}

// TestNew_RejectsZeroValueDrain asserts New rejects a non-nil but zero-value
// DrainSignal (new(grpcadapter)... is impossible — the type is in runtimegrpc):
// new(runtimegrpc.DrainSignal) passes a bare `== nil` check but has a nil cancel
// and would panic at GracefulStop, so Config.validate calls Drain.Validate().
func TestNew_RejectsZeroValueDrain(t *testing.T) {
	deps := readyzDeps()
	deps.Drain = new(runtimegrpc.DrainSignal) // non-nil zero-value → invalid
	_, err := grpcadapter.New(grpcadapter.Config{
		Addr:         ":0",
		TLS:          grpcadapter.TLSConfig{AllowInsecure: true},
		Interceptors: deps,
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
		Interceptors: readyzDeps(),
	})
	require.NoError(t, err)

	probes := srv.Probes()
	require.Len(t, probes, 1, "gRPC server exposes exactly one readiness probe")
	require.Equal(t, grpcadapter.ProbeReady, probes[0].Name())
	require.Error(t, probes[0].Check(context.Background()),
		"grpc_ready must report unhealthy before the server is serving")
}
