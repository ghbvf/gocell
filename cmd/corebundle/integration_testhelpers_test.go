//go:build integration

package main

import (
	"context"
	"errors"
	"net"
	"testing"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/auth/authtest"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"

	"github.com/stretchr/testify/require"

	cellmodulesconfigcore "github.com/ghbvf/gocell/cellmodules/configcore"
	"github.com/ghbvf/gocell/cellmodules/deviceserving"
	"github.com/ghbvf/gocell/cellmodules/grpclistener"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// corebundleTestNoopVerifier is a stub gRPC bearer verifier for boot harnesses that
// wire the (mandatory) gRPC listener but never call it. accesscore registers
// grpc.auth.session.verify.v1 unconditionally (cell_gen.go, PR-11 #1154), so every
// assembly that boots it MUST wire a gRPC listener or bootstrap fail-fasts
// (checkOrphanGRPCServices). HTTP-focused integration harnesses don't exercise gRPC,
// so a verifier that rejects every token suffices. The dedicated gRPC end-to-end test
// uses a real verifier.
type corebundleTestNoopVerifier struct{}

func (corebundleTestNoopVerifier) VerifyIntent(context.Context, string, kauth.TokenIntent) (kauth.Claims, error) {
	return kauth.Claims{}, errors.New("grpc bearer auth not exercised in this harness")
}

// corebundleTestGRPCListenerOption builds a WithGRPCListener option backed by an
// ephemeral pre-bound socket + a real interceptor chain (the cells' lazy PDP
// authorizer + a no-op bearer verifier), mirroring the production run.go gRPC wiring.
// Every harness that boots the full corebundle assembly (which includes accesscore)
// must include it, otherwise accesscore's unconditional grpc.auth.session.verify.v1
// registration trips the orphan-grpc startup fail-fast. cellIDs is the metrics
// closed set (asm.CellIDs()).
func corebundleTestGRPCListenerOption(t *testing.T, cells []cell.Cell, cellIDs []string) bootstrap.Option {
	t.Helper()
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcLn.Close() })

	authorizer, err := bootstrap.AuthorizerFromCells(cells)
	require.NoError(t, err)
	collector, err := obmetrics.NewGRPCProviderCollector(kernelmetrics.NopProvider{}, obmetrics.ProviderCollectorConfig{})
	require.NoError(t, err)
	grpcServer, err := grpclistener.ServerFromEnv(outbox.DurabilityDemo, grpcLn.Addr().String(), interceptor.Deps{
		Verifier:        corebundleTestNoopVerifier{},
		Clock:           clock.Real(),
		Collector:       collector,
		Authorizer:      authorizer,
		MetricsProvider: kernelmetrics.NopProvider{},
		CellIDClosedSet: cellIDs,
	})
	require.NoError(t, err)
	return bootstrap.WithGRPCListener(cell.PrimaryListener, grpcServer, grpcLn.Addr().String(), bootstrap.WithGRPCListenerNet(grpcLn))
}

// runner is the minimal interface shared by *bootstrap.Bootstrap and
// *composition.App, both of which expose Run(ctx) error. Tests use this to
// avoid coupling to a concrete type.
type runner interface {
	Run(ctx context.Context) error
}

// buildTestSharedDeps returns a minimal *composition.SharedDeps suitable for
// memory topology integration tests. It wraps buildTestSharedDepsAndLocals,
// which is defined in bundle_test.go (non-integration).
func buildTestSharedDeps(t *testing.T) *composition.SharedDeps {
	t.Helper()
	shared, _ := buildTestSharedDepsAndLocals(t)
	return shared
}

// buildBootstrapFromShared is the test-path assembly helper equivalent to the
// production runCorebundle flow. It builds the full corebundle assembly using
// the memory topology and returns an app that can be started via Run. Tests
// supply the primary net.Listener and any extra options (typically
// WithListener for InternalListener/HealthListener, etc.).
//
// Uses memory topology (from the shared argument) and the generated platform
// modules (configcore → auditcore → accesscore).
func buildBootstrapFromShared(
	t *testing.T, shared *composition.SharedDeps, primaryLn net.Listener, extra ...bootstrap.Option,
) (runner, error) {
	t.Helper()
	ctx := context.Background()
	_, locals := buildTestSharedDepsAndLocals(t)

	// Propagate caller mutations (e.g. shared.MetricsToken) by using the
	// caller-provided shared directly in the runtimeOptsFunc closure.
	mods := generatedCellModules()

	// runtimeOptsFunc mirrors the production runCorebundle path but simplified for
	// testing: no real-mode validation, inline listener construction.
	runtimeOptsFunc := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		asm, err := buildAssembly(locals, "corebundle-test",
			durabilityModeForTopology(shared.Topology), shared.Clock, generatedFrameworkServedContracts(), cells...)
		if err != nil {
			return nil, err
		}

		consumerBase, err := buildConsumerBase(shared)
		if err != nil {
			return nil, err
		}

		adapterInfo := adapterInfoForSharedDeps(shared, locals)
		metricsHandler := buildMetricsHandler(shared.MetricsToken, locals.registry)
		opts := runtimeBaseOptions(shared, locals, asm, consumerBase, metricsHandler, adapterInfo)

		// Primary listener (JWT policy resolved from assembly, F3 round-3 collapse) +
		// the mandatory gRPC listener — accesscore registers grpc.auth.session.verify.v1
		// unconditionally (PR-11 #1154), so a gRPC listener must be wired or bootstrap
		// fail-fasts (checkOrphanGRPCServices).
		opts = append(opts,
			bootstrap.WithListener(
				cell.PrimaryListener,
				primaryLn.Addr().String(),
				[]kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
				bootstrap.WithListenerNet(primaryLn),
			),
			corebundleTestGRPCListenerOption(t, cells, asm.CellIDs()),
			// Framework-owned HTTP serving mirrors production runCorebundle: the
			// assembly above carries generatedFrameworkServedContracts() as its
			// must-serve set, so this option MUST wire the matching routes or phase0
			// validateFrameworkServing fail-fasts. Keeping the test harness in lockstep
			// with production wiring is the #2348 review F2 fix (omit → caught here).
			bootstrap.WithFrameworkHTTPServing(
				[]bootstrap.FrameworkServedRoute{deviceserving.NewService(shared.Clock).Route()},
			),
		)
		opts = append(opts, extra...)
		return opts, nil
	}

	return composition.New(corebundleCellIDs()...).With(mods...).Build(ctx, shared, runtimeOptsFunc)
}

// corebundleCellIDs returns the cell-id closed set for the corebundle assembly,
// derived from generatedCellModules() so integration tests satisfy
// composition.Builder.Build's M12a closed-set guard without hardcoding the cell
// list. In production the set comes from the generated main.go's assembly cell
// slice; both are derived from assembly.yaml.
func corebundleCellIDs() []string {
	mods := generatedCellModules()
	ids := make([]string, len(mods))
	for i, m := range mods {
		ids[i] = m.ID()
	}
	return ids
}

// withCorebundleTestInternalListener returns a bootstrap.Option that registers
// an InternalListener backed by the provided net.Listener, wired with the
// cmd-test internal auth chain (buildInternalAuthChain from SharedDeps).
func withCorebundleTestInternalListener(t *testing.T, ln net.Listener) bootstrap.Option {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
	require.NoError(t, err)
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	shared := &composition.SharedDeps{
		InternalServiceKeyring: ring,
		NonceStore:             nonceStore,
	}
	chain, err := buildInternalAuthChain(shared)
	require.NoError(t, err)
	return bootstrap.WithListener(
		cell.InternalListener,
		ln.Addr().String(),
		chain,
		bootstrap.WithListenerNet(ln),
	)
}

// entryToSubHandler wraps a business EntryHandler as a SubscriberHandler with
// nil Settlement. Tests call eb.Subscribe directly and do not need ConsumerBase
// idempotency — nil Settlement is safe because the subscriber nil-checks before
// calling Commit/Release.
//
// This helper replaces the deleted outbox.EntryToSubscriberHandler in test-only
// code (mirroring runtime/eventbus/entry_lift_test.go::entryToSubHandler).
// Production callers use SubscriberWithMiddleware.SubscribeEntry instead.
func entryToSubHandler(h outbox.EntryHandler) outbox.SubscriberHandler {
	return func(ctx context.Context, entry outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		r := h(ctx, entry)
		return outbox.DeliveryOutcome{Disposition: r.Disposition, Err: r.Err}, nil
	}
}

// discardPublisher is a minimal outbox.Publisher for wiring tests that need
// an in-memory publisher without any delivery guarantee.
type discardPublisher struct{}

func (discardPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (discardPublisher) Close(_ context.Context) error                       { return nil }

var _ outbox.Publisher = discardPublisher{}

// configCoreProvideResult mirrors the result shape of the old buildConfigCoreOpts
// (pre-platform-migration). Integration tests use it to get configcore cell opts +
// relay bootstrap opts without direct access to the now-unexported cellmodules/configcore
// internals.
type configCoreProvideResult struct {
	// Cell is the pre-built *configcore.ConfigCore cell.
	Cell cell.Cell
	// BootstrapOpts carries relay options (WithRelay in postgres mode, empty in memory mode).
	BootstrapOpts []bootstrap.Option
	// Resources lists provisional ManagedResources (KeyProvider in vault mode; empty otherwise).
	Resources []kernellifecycle.ManagedResource
}

// buildConfigCoreCellFromShared calls cellmodules/configcore.Module().Provide(ctx, shared)
// and returns the result in a test-friendly struct. Tests that need the
// pre-built configcore cell + relay bootstrap opts use this instead of the
// deleted cmd-internal buildConfigCoreOpts + ConfigCoreModuleConfig path.
//
// shared.PG must already be set when postgres mode is needed (caller uses
// capability.NewPGProvider after opening a pool). The caller must also set
// the env vars consumed by the module (GOCELL_CONFIGCORE_CURSOR_KEY,
// GOCELL_CONFIGCORE_KEY_PROVIDER, etc.) — typically via setRealModeEnv or
// t.Setenv.
func buildConfigCoreCellFromShared(
	t testing.TB, ctx context.Context, shared *composition.SharedDeps,
) configCoreProvideResult {
	t.Helper()
	mr, err := cellmodulesconfigcore.Module().Provide(ctx, shared)
	require.NoError(t, err, "cellmodules/configcore.Module().Provide must succeed")
	require.NotNil(t, mr.Cell, "configcore cell must be non-nil")
	return configCoreProvideResult{
		Cell:          mr.Cell,
		BootstrapOpts: mr.Opts,
		Resources:     mr.Resources,
	}
}
