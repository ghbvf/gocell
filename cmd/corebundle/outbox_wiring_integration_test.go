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
	"github.com/ghbvf/gocell/runtime/crypto"
)

// TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil asserts that postgres mode
// produces a non-nil ManagedResource (pool) and a non-empty bootstrapOpts slice
// carrying WithManagedResource(relay). The test self-provisions PostgreSQL via
// testcontainers so CI cannot go green by omitting external DSN configuration.
//
// The relay is now registered independently via bootstrapOpts rather than
// carried inside PGResource.Worker() — PGResource wraps only the pool.
func TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil(t *testing.T) {
	ctx := context.Background()
	pgDSN, cleanup := setupPostgresForMain(t)
	defer cleanup()
	applyMigrationsForMain(t, ctx, pgDSN)

	topo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"}
	pgCfg := adapterpg.Config{DSN: pgDSN}
	result, err := buildConfigCoreOpts(ctx, ConfigCoreModuleConfig{
		Topology:         topo,
		PGConfig:         pgCfg,
		Publisher:        discardPublisher{},
		MetricsProvider:  metrics.NopProvider{},
		ValueTransformer: crypto.NoopTransformer{},
		Clock:            clock.Real(),
	})

	require.NoError(t, err, "postgres mode must not error when DSN is valid")
	require.NotNil(t, result.PGResource, "postgres mode must return a non-nil ManagedResource (pool)")
	assert.NotEmpty(t, result.CellOptions, "postgres mode must return cell options")

	// PGResource.Worker() must be nil — pool has no background worker.
	assert.Nil(t, result.PGResource.Worker(), "PGResource must not carry a relay worker; relay is registered via bootstrapOpts")

	// The relay is independently registered via bootstrap opts (A11 fix).
	assert.NotEmpty(t, result.BootstrapOpts, "postgres mode must return bootstrap opts carrying relay ManagedResource")

	// Close the pool via ManagedResource.Close(ctx) so pool.Close(ctx) is called.
	require.NoError(t, result.PGResource.Close(context.Background()))
}
