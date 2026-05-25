//go:build integration

package saga_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration040_CreatesSagaTables verifies migration 040 produces the two
// expected relations with PK + lease columns. pgshare's NewPerTestPool runs
// the full migration set (014..040) on first call; this test just inspects
// the resulting schema via pg_catalog.
//
// The verification proves migration 040 was applied — saga_instances /
// saga_events would not exist on a fresh template DB otherwise.
func TestMigration040_CreatesSagaTables(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	ctx := context.Background()

	relations := []string{"saga_instances", "saga_events"}
	for _, rel := range relations {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_class c
				JOIN pg_namespace n ON c.relnamespace = n.oid
				WHERE c.relname = $1 AND n.nspname = 'public' AND c.relkind = 'r')`, rel).Scan(&exists)
		require.NoError(t, err, "pg_catalog probe for %s", rel)
		require.True(t, exists, "migration 040 should create %s", rel)
	}

	// Lease columns must exist on saga_instances.
	leaseCols := []string{"lease_id", "lease_expires_at"}
	for _, col := range leaseCols {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'saga_instances'
				  AND column_name = $1)`, col).Scan(&exists)
		require.NoError(t, err, "information_schema probe for column %s", col)
		require.True(t, exists, "saga_instances must have column %s", col)
	}

	// PK on saga_events must be (instance_id, version).
	var pkDef string
	err := pool.DB().QueryRow(ctx,
		`SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
			JOIN pg_class rel ON c.conrelid = rel.oid
			WHERE rel.relname = 'saga_events' AND c.contype = 'p'`).Scan(&pkDef)
	require.NoError(t, err, "pg_get_constraintdef for saga_events PK")
	require.Contains(t, pkDef, "instance_id", "saga_events PK must include instance_id")
	require.Contains(t, pkDef, "version", "saga_events PK must include version")

	// NOT NULL coverage for saga_instances core columns.
	notNullCols := []string{"status", "current_version", "started_at", "updated_at"}
	for _, col := range notNullCols {
		var isNullable string
		err := pool.DB().QueryRow(ctx,
			`SELECT is_nullable FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'saga_instances'
				  AND column_name = $1`, col).Scan(&isNullable)
		require.NoError(t, err, "information_schema lookup for %s", col)
		require.Equal(t, "NO", isNullable, "saga_instances.%s must be NOT NULL", col)
	}

	// CHECK constraints on saga_instances (status range + lease paired).
	checkConstraints := []string{
		"saga_instances_status_range",
		"saga_instances_lease_paired",
		"saga_instances_version_nonneg",
	}
	for _, ck := range checkConstraints {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_constraint c
				JOIN pg_class rel ON c.conrelid = rel.oid
				WHERE rel.relname = 'saga_instances' AND c.conname = $1 AND c.contype = 'c')`,
			ck).Scan(&exists)
		require.NoError(t, err, "pg_constraint lookup for %s", ck)
		require.True(t, exists, "saga_instances must have CHECK constraint %s", ck)
	}

	// FK from saga_events.instance_id → saga_instances.id with ON DELETE CASCADE.
	var fkDef string
	err = pool.DB().QueryRow(ctx,
		`SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
			JOIN pg_class rel ON c.conrelid = rel.oid
			WHERE rel.relname = 'saga_events' AND c.contype = 'f'`).Scan(&fkDef)
	require.NoError(t, err, "pg_get_constraintdef for saga_events FK")
	require.Contains(t, fkDef, "saga_instances", "saga_events FK must reference saga_instances")
	require.Contains(t, fkDef, "ON DELETE CASCADE", "saga_events FK must specify ON DELETE CASCADE")

	// Partial index for ClaimPending.
	var idxExists bool
	err = pool.DB().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public'
			  AND tablename = 'saga_instances'
			  AND indexname = 'idx_saga_instances_claimable')`).Scan(&idxExists)
	require.NoError(t, err, "pg_indexes lookup for idx_saga_instances_claimable")
	require.True(t, idxExists, "saga_instances must have idx_saga_instances_claimable partial index")
}
