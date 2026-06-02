//go:build integration

package main

import (
	"context"
	"net"
	"testing"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/auth/authtest"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"

	"github.com/stretchr/testify/require"

	cellmodulesconfigcore "github.com/ghbvf/gocell/cellmodules/configcore"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
)

// runnable is the minimal interface shared by *bootstrap.Bootstrap and
// *composition.App, both of which expose Run(ctx) error. Tests use this to
// avoid coupling to a concrete type.
type runnable interface {
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
) (runnable, error) {
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
			durabilityModeForTopology(shared.Topology), shared.Clock, cells...)
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

		// Primary listener: JWT policy resolved from assembly (F3 round-3 collapse).
		opts = append(opts, bootstrap.WithListener(
			cell.PrimaryListener,
			primaryLn.Addr().String(),
			[]kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(primaryLn),
		))
		opts = append(opts, extra...)
		return opts, nil
	}

	return composition.New().With(mods...).Build(ctx, shared, runtimeOptsFunc)
}

// withCorebundleTestInternalListener returns a bootstrap.Option that registers
// an InternalListener backed by the provided net.Listener, wired with the
// cmd-test internal auth chain (buildInternalAuthChain + a fresh internalGuard).
func withCorebundleTestInternalListener(t *testing.T, ln net.Listener) bootstrap.Option {
	t.Helper()
	chain, err := buildInternalAuthChain(newTestInternalGuard(t))
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
	return func(ctx context.Context, entry outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
		return h(ctx, entry), nil
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
	c, opts, res, err := cellmodulesconfigcore.Module().Provide(ctx, shared)
	require.NoError(t, err, "cellmodules/configcore.Module().Provide must succeed")
	require.NotNil(t, c, "configcore cell must be non-nil")
	return configCoreProvideResult{
		Cell:          c,
		BootstrapOpts: opts,
		Resources:     res,
	}
}
