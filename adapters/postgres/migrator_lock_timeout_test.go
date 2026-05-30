//go:build integration

package postgres

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3/lock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrator_LockTimeoutSessionLocker_SetsAndResets verifies that the
// lock_timeout session locker sets lock_timeout (session scope) on the goose
// connection for the duration of a migration session and resets it on unlock so
// the connection is clean when returned to the shared pgxpool. Session scope
// (not SET LOCAL) is required so the timeout survives across the implicit-tx
// boundaries of `-- +goose no transaction` migrations, mirroring the
// session-scope lock_timeout requirement for `-- +goose no transaction` migrations.
func TestMigrator_LockTimeoutSessionLocker_SetsAndResets(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	db := stdlib.OpenDBFromPool(pool.inner)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	inner, err := lock.NewPostgresSessionLocker()
	require.NoError(t, err)
	locker := &lockTimeoutSessionLocker{inner: inner}

	// Capture the pre-lock value so the reset assertion compares against the
	// server's actual default rather than hardcoding "0" (RESET restores the
	// startup/postgresql.conf value, which need not be 0).
	var preLock string
	require.NoError(t,
		conn.QueryRowContext(ctx, `SELECT current_setting('lock_timeout')`).Scan(&preLock))

	require.NoError(t, locker.SessionLock(ctx, conn))
	var lt string
	require.NoError(t,
		conn.QueryRowContext(ctx, `SELECT current_setting('lock_timeout')`).Scan(&lt))
	assert.Equal(t, migrationLockTimeout, lt,
		"lock_timeout must be set for the duration of the migration session")

	require.NoError(t, locker.SessionUnlock(ctx, conn))
	require.NoError(t,
		conn.QueryRowContext(ctx, `SELECT current_setting('lock_timeout')`).Scan(&lt))
	assert.Equal(t, preLock, lt, "lock_timeout must be reset to its pre-lock value after unlock")
}

// TestMigrator_LockTimeoutAppliedViaProvider guards the wiring (not just the
// locker type): it runs probe migrations through the REAL
// NewMigrator(...).Up(ctx) → newGooseProvider → goose → SessionLock path, where
// each migration body RAISEs unless current_setting('lock_timeout') = '5s'. If
// the lockTimeoutSessionLocker wrapper in newGooseProvider were removed, the
// session would carry the default lock_timeout and Up would fail here — which
// TestMigrator_LockTimeoutSessionLocker_SetsAndResets (constructs the locker
// directly) cannot catch. Covers both a transactional and a
// `-- +goose no transaction` migration, locking the PR's session-scope claim
// across implicit-tx boundaries.
func TestMigrator_LockTimeoutAppliedViaProvider(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	const txnProbe = `-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('lock_timeout') IS DISTINCT FROM '5s' THEN
        RAISE EXCEPTION 'lock_timeout not injected (txn): got %', current_setting('lock_timeout');
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
`
	const noTxnProbe = `-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('lock_timeout') IS DISTINCT FROM '5s' THEN
        RAISE EXCEPTION 'lock_timeout not injected (no-txn): got %', current_setting('lock_timeout');
    END IF;
END $$;
-- +goose StatementEnd
`
	fixtureFS := fstest.MapFS{
		"001_locktimeout_probe_txn.sql":   &fstest.MapFile{Data: []byte(txnProbe)},
		"002_locktimeout_probe_notxn.sql": &fstest.MapFile{Data: []byte(noTxnProbe)},
	}

	migrator, err := NewMigrator(pool, fixtureFS, "schema_migrations_locktimeout_probe")
	require.NoError(t, err)
	t.Cleanup(func() { _ = migrator.Close() })

	require.NoError(t, migrator.Up(ctx),
		"probe migrations RAISE unless lock_timeout='5s' is injected by "+
			"lockTimeoutSessionLocker via newGooseProvider — guards the wiring, "+
			"not just the locker type")
}

// TestMigrator_LockTimeoutAppliedViaProvider_Down verifies that the Down path
// also runs through lockTimeoutSessionLocker. newGooseProvider injects the
// locker once at construction time and goose uses it for both Up and Down
// sessions; this test guards that wiring for rollbacks so a future refactor
// that accidentally splits the provider path cannot silently drop the
// lock_timeout injection on Down.
func TestMigrator_LockTimeoutAppliedViaProvider_Down(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// The Down probe RAISEs unless lock_timeout = '5s'. The Up body is a no-op
	// CREATE TABLE so the migration is both reversible and leaves no rows that
	// would require a ForwardRebuild permit.
	const downProbe = `-- +goose Up
CREATE TABLE IF NOT EXISTS _locktimeout_down_probe (id int);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('lock_timeout') IS DISTINCT FROM '5s' THEN
        RAISE EXCEPTION 'lock_timeout not injected (down): got %', current_setting('lock_timeout');
    END IF;
END $$;
DROP TABLE IF EXISTS _locktimeout_down_probe;
-- +goose StatementEnd
`
	fixtureFS := fstest.MapFS{
		"001_locktimeout_down_probe.sql": &fstest.MapFile{Data: []byte(downProbe)},
	}

	migrator, err := NewMigrator(pool, fixtureFS, "schema_migrations_locktimeout_down_probe")
	require.NoError(t, err)
	t.Cleanup(func() { _ = migrator.Close() })

	require.NoError(t, migrator.Up(ctx), "Up must succeed before Down can be tested")

	permit, err := AllowDestructiveDown("lock_timeout Down probe test")
	require.NoError(t, err)

	require.NoError(t, migrator.Down(ctx, permit),
		"Down probe RAISEs unless lock_timeout='5s' is injected by "+
			"lockTimeoutSessionLocker via newGooseProvider — guards the Down wiring")
}
