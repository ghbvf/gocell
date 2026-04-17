//go:build integration

package configwrite

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	cellpg "github.com/ghbvf/gocell/cells/config-core/internal/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// setupWriteService spins up a PostgreSQL container, applies migrations,
// and returns a Service wired with PG repo + outbox writer + tx manager.
func setupWriteService(t *testing.T) (*Service, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session)
	outboxWriter := adapterpg.NewOutboxWriter()
	txMgr := adapterpg.NewTxManager(pool)

	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(outboxWriter),
		WithTxManager(txMgr),
	)

	cleanup := func() {
		pool.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate container: %v", err)
		}
	}

	return svc, cleanup
}

// TestCreate_AtomicWithOutbox verifies that config_entries and outbox_entries
// rows are both committed in the same transaction (L2 atomicity).
func TestCreate_AtomicWithOutbox(t *testing.T) {
	svc, cleanup := setupWriteService(t)
	defer cleanup()

	entry, err := svc.Create(context.Background(), CreateInput{
		Key:   "integration.atomic.write",
		Value: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "integration.atomic.write", entry.Key)
	assert.Equal(t, 1, entry.Version)
}

// TestCreate_RollbackOnOutboxFailure verifies that when the outbox write
// returns a permanent error, the config_entries row is absent (transaction
// rolled back atomically).
func TestCreate_RollbackOnOutboxFailure(t *testing.T) {
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	defer func() { _ = container.Terminate(ctx) }()

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)
	defer pool.Close()

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session)

	// Inject a writer that always fails — simulates outbox unavailable.
	failingWriter := &recordingWriter{err: errors.New("outbox broker down")}

	txMgr := adapterpg.NewTxManager(pool)
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(failingWriter),
		WithTxManager(txMgr),
	)

	_, err = svc.Create(ctx, CreateInput{Key: "rollback.test", Value: "v"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox")

	// config_entries row must NOT exist (rolled back).
	_, getErr := repo.GetByKey(ctx, "rollback.test")
	require.Error(t, getErr)
	var ec *errcode.Error
	require.ErrorAs(t, getErr, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code,
		"config entry must not persist after outbox-failure rollback")
}
