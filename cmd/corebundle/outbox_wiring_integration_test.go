//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil asserts that postgres mode
// produces a non-nil ManagedResource (pool) and a non-empty bootstrapOpts slice
// carrying WithManagedResource(relay). This test requires a running PostgreSQL
// instance (GOCELL_CONFIGCORE_DATABASE_URL must be set).
//
// The relay is now registered independently via bootstrapOpts rather than
// carried inside PGResource.Worker() — PGResource wraps only the pool.
func TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil(t *testing.T) {
	pgDSN, ok := os.LookupEnv("GOCELL_CONFIGCORE_DATABASE_URL")
	if !ok || pgDSN == "" {
		t.Skip("GOCELL_CONFIGCORE_DATABASE_URL not set; skipping PG-mode relay wiring integration test")
	}

	ctx := context.Background()
	topo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"}
	pgCfg := adapterpg.Config{DSN: pgDSN}
	res, cellOpts, bootstrapOpts, err := buildConfigCoreOpts(ctx, topo, pgCfg, discardPublisher{}, metrics.NopProvider{}, crypto.NoopTransformer{})

	require.NoError(t, err, "postgres mode must not error when DSN is valid")
	require.NotNil(t, res, "postgres mode must return a non-nil ManagedResource (pool)")
	assert.NotEmpty(t, cellOpts, "postgres mode must return cell options")

	// PGResource.Worker() must be nil — pool has no background worker.
	assert.Nil(t, res.Worker(), "PGResource must not carry a relay worker; relay is registered via bootstrapOpts")

	// The relay is independently registered via bootstrap opts (A11 fix).
	assert.NotEmpty(t, bootstrapOpts, "postgres mode must return bootstrap opts carrying relay ManagedResource")

	// Close the pool via ManagedResource.Close(ctx) so pool.Close(ctx) is called.
	require.NoError(t, res.Close(context.Background()))
}
