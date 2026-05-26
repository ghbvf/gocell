//go:build integration

package postgres

import (
	"context"
	"testing"

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
// boundaries of `-- +goose no transaction` migrations, mirroring
// destructiveDownSessionLocker.
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

	require.NoError(t, locker.SessionLock(ctx, conn))
	var lt string
	require.NoError(t,
		conn.QueryRowContext(ctx, `SELECT current_setting('lock_timeout')`).Scan(&lt))
	assert.Equal(t, migrationLockTimeout, lt,
		"lock_timeout must be set for the duration of the migration session")

	require.NoError(t, locker.SessionUnlock(ctx, conn))
	require.NoError(t,
		conn.QueryRowContext(ctx, `SELECT current_setting('lock_timeout')`).Scan(&lt))
	assert.Equal(t, "0", lt, "lock_timeout must be reset to default (0) after unlock")
}
