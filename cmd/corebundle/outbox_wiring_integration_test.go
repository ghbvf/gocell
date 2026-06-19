//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/capability"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil asserts that postgres mode
// produces a non-nil app from composition.Builder.Build. The test self-provisions
// PostgreSQL via testcontainers so CI cannot go green by omitting external DSN
// configuration.
//
// After #2341, configcore no longer contributes relay bootstrap opts; relay
// registration is handled by cap_wiring (composition root) and its assertion lives
// in corebundle_pg_env_integration_test.go (locals.relayOpts non-empty). This test
// only verifies that postgres-mode Build succeeds (app != nil).
//
// Pool provisioning has moved to provisionCapabilities; this test opens a pool
// directly, wraps it into a capability.PGProvider, and injects it via a minimal
// composition.SharedDeps so the platform modules can Provide without env-var setup.
func TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil(t *testing.T) {
	ctx := context.Background()
	pgDSN, cleanup := setupPostgresForMain(t)
	defer cleanup()
	applyMigrationsForMain(t, ctx, pgDSN)

	// Set minimal env vars for the platform modules that read env at Provide time
	// (cursor keys, HMAC keys, etc.).
	setRealModeEnv(t, pgDSN)

	shared, locals, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	// Open pool, wrap into PGProvider, and inject directly so provisionCapabilities
	// does not open a second pool from env.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: pgDSN})
	require.NoError(t, err, "open PG pool for test")
	defer func() { _ = pool.Close(ctx) }()

	txMgr := adapterpg.NewTxManager(pool)
	writer := adapterpg.NewOutboxWriter(shared.Clock)
	// #2341: shared.PG is a per-cell PGSet; wrap the one pool as the colocated
	// provider serving every postgres cell.
	prov := capability.NewPGProvider(txMgr, writer, pool.DB())
	pgSet, err := capability.NewPGSet([]capability.PGInstance{{Provider: prov, Cells: generatedPostgresCells()}})
	require.NoError(t, err, "build colocated PGSet")
	shared.PG = pgSet

	mods := generatedCellModules()
	_ = locals // locals used only for shared deps construction, not module wiring
	app, buildErr := composition.New(corebundleCellIDs()...).With(mods...).Build(ctx, shared,
		func(cells []cell.Cell) ([]bootstrap.Option, error) {
			_ = cells
			return nil, nil
		})
	require.NoError(t, buildErr, "postgres mode must not error when DSN is valid")
	assert.NotNil(t, app, "postgres mode must return a non-nil App")
}
