//go:build integration

package postgres

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/migration"
)

// extWidgetsFS is a synthetic external Cell module migration set: its own
// independent 001 sequence, goose-native naming, creating a table that does not
// collide with any platform table.
func extWidgetsFS() fstest.MapFS {
	return fstest.MapFS{
		"001_create_ext_widgets.sql": &fstest.MapFile{Data: []byte(
			"-- +goose Up\n" +
				"CREATE TABLE IF NOT EXISTS ext_widgets (id TEXT PRIMARY KEY);\n" +
				"-- +goose Down\n" +
				"DROP TABLE IF EXISTS ext_widgets;\n")},
	}
}

// TestMigrationSet_ApplyAll_IndependentNamespaceTables dogfoods the full #1089
// path end to end: platform + a synthetic external namespace apply against their
// OWN tracking tables (schema_migrations_platform / schema_migrations_extwidgets),
// each with an independent version lineage, and ApplyAll is idempotent.
func TestMigrationSet_ApplyAll_IndependentNamespaceTables(t *testing.T) {
	ctx := context.Background()
	pool := emptyPool(t)

	ext, err := migration.ParseNamespace("extwidgets")
	require.NoError(t, err)

	set, err := NewMigrationSetWithPlatform()
	require.NoError(t, err)
	require.NoError(t, set.Add(ext, extWidgetsFS()))

	// platform-first ordering guarantee.
	require.Equal(t, []migration.Namespace{migration.PlatformNamespace, ext}, set.Namespaces())

	require.NoError(t, set.ApplyAll(ctx, pool), "apply platform + external")

	// Both per-namespace tracking tables exist and are physically distinct.
	assertTableExists(ctx, t, pool, "schema_migrations_platform")
	assertTableExists(ctx, t, pool, "schema_migrations_extwidgets")
	assert.False(t, tableExists(ctx, t, pool, "schema_migrations"),
		"the bare global schema_migrations table must NOT exist (R3 sealed)")

	// The external migration ran against the same DB as platform.
	assertTableExists(ctx, t, pool, "ext_widgets")

	// Independent version lineages: platform at its embedded max, external at 1.
	platformFS, err := MigrationsFS()
	require.NoError(t, err)
	platformMax, err := ExpectedVersion(platformFS)
	require.NoError(t, err)
	assert.Equal(t, platformMax, maxTrackedVersion(ctx, t, pool, "schema_migrations_platform"))
	assert.Equal(t, int64(1), maxTrackedVersion(ctx, t, pool, "schema_migrations_extwidgets"),
		"external namespace keeps its own 001..N sequence, not a global one")

	// VerifyAll confirms each namespace's DB version matches its embedded max.
	require.NoError(t, set.VerifyAll(ctx, pool))

	// Idempotent: re-applying is a no-op (no pending migrations in either table).
	require.NoError(t, set.ApplyAll(ctx, pool), "re-apply must be a no-op")
}

// TestMigrationSet_VerifyAll_FailsWhenNamespaceUnapplied covers the failure path:
// VerifyAll over a namespace whose migrations have NOT been applied must fail,
// and the error must name the offending namespace (F3 — attributable VerifyAll).
func TestMigrationSet_VerifyAll_FailsWhenNamespaceUnapplied(t *testing.T) {
	ctx := context.Background()
	pool := emptyPool(t)

	ext, err := migration.ParseNamespace("extunapplied")
	require.NoError(t, err)

	// Apply ONLY platform.
	platformOnly, err := NewMigrationSetWithPlatform()
	require.NoError(t, err)
	require.NoError(t, platformOnly.ApplyAll(ctx, pool))

	// VerifyAll over platform + an unapplied external namespace must fail and
	// the wrapped error must name the offending namespace.
	full, err := NewMigrationSetWithPlatform()
	require.NoError(t, err)
	require.NoError(t, full.Add(ext, extWidgetsFS()))

	err = full.VerifyAll(ctx, pool)
	require.Error(t, err, "VerifyAll must fail when an external namespace is unapplied")
	assert.Contains(t, err.Error(), "extunapplied",
		"VerifyAll error must name the unapplied namespace (F3)")
}

func tableExists(ctx context.Context, t *testing.T, pool *Pool, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.DB().QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists))
	return exists
}

func assertTableExists(ctx context.Context, t *testing.T, pool *Pool, table string) {
	t.Helper()
	assert.Truef(t, tableExists(ctx, t, pool, table), "expected table %q to exist", table)
}

func maxTrackedVersion(ctx context.Context, t *testing.T, pool *Pool, trackingTable string) int64 {
	t.Helper()
	if err := validateIdentifier(trackingTable); err != nil {
		t.Fatalf("invalid tracking table %q: %v", trackingTable, err)
	}
	var maxV int64
	// #nosec G201 -- trackingTable validated by validateIdentifier above.
	q := `SELECT COALESCE(MAX(version_id), 0) FROM ` + trackingTable
	require.NoError(t, pool.DB().QueryRow(ctx, q).Scan(&maxV))
	return maxV
}
