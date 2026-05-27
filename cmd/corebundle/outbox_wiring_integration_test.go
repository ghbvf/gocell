//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil asserts that postgres mode
// produces non-nil cell options and a non-empty bootstrapOpts slice carrying
// WithRelay. The test self-provisions PostgreSQL via testcontainers so CI cannot
// go green by omitting external DSN configuration.
//
// Pool provisioning has moved to provisionCapabilities; this test opens a pool
// directly, wraps it into a capability.PGProvider, and injects it. The relay is
// registered via bootstrap opts, not via PoolResource.
func TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil(t *testing.T) {
	ctx := context.Background()
	pgDSN, cleanup := setupPostgresForMain(t)
	defer cleanup()
	applyMigrationsForMain(t, ctx, pgDSN)

	topo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"}

	// Open pool, wrap into PGProvider, and inject into buildConfigCoreOpts.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: pgDSN})
	require.NoError(t, err, "open PG pool for test")
	defer func() { _ = pool.Close(ctx) }()

	txMgr := adapterpg.NewTxManager(pool)
	writer := adapterpg.NewOutboxWriter(clock.Real())
	pgProvider := capability.NewPGProvider(txMgr, writer, pool.DB())

	result, err := buildConfigCoreOpts(clock.Real(), ConfigCoreModuleConfig{
		Topology:         topo,
		PG:               pgProvider,
		Publisher:        discardPublisher{},
		MetricsProvider:  metrics.NopProvider{},
		ValueTransformer: crypto.NoopTransformer{},
	})

	require.NoError(t, err, "postgres mode must not error when DSN is valid")
	assert.NotEmpty(t, result.CellOptions, "postgres mode must return cell options")

	// The relay is independently registered via bootstrap opts (A11 fix).
	assert.NotEmpty(t, result.BootstrapOpts, "postgres mode must return bootstrap opts carrying relay ManagedResource")
}
