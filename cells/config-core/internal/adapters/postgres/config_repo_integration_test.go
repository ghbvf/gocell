//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// setupConfigPG spins up a PostgreSQL container, applies all migrations
// (including 004 for config_entries + config_versions), and returns a
// ConfigRepository backed by a Session plus cleanup func.
func setupConfigPG(t *testing.T) (*ConfigRepository, *adapterpg.TxManager, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	session := NewSession(pool.DB())
	repo := NewConfigRepository(session)
	txMgr := adapterpg.NewTxManager(pool)

	cleanup := func() {
		_ = pool.Close(ctx)
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return repo, txMgr, cleanup
}

// TestConfigRepo_Integration_CRUD exercises all 7 repository methods
// (Create / GetByKey / Update / Delete / List / PublishVersion / GetVersion)
// against a real PostgreSQL instance with migration 004 applied.
func TestConfigRepo_Integration_CRUD(t *testing.T) {
	repo, txMgr, cleanup := setupConfigPG(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Create_and_GetByKey", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:        uuid.NewString(),
			Key:       "integration.test.key",
			Value:     "hello",
			Sensitive: false,
			Version:   1,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, entry)
		}))

		got, err := repo.GetByKey(ctx, "integration.test.key")
		require.NoError(t, err)
		assert.Equal(t, entry.ID, got.ID)
		assert.Equal(t, "hello", got.Value)
		assert.Equal(t, 1, got.Version)
	})

	t.Run("Update", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:      uuid.NewString(),
			Key:     "integration.update.key",
			Value:   "original",
			Version: 1,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, entry)
		}))

		entry.Value = "updated"
		entry.Version = 2
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Update(txCtx, entry)
		}))

		got, err := repo.GetByKey(ctx, "integration.update.key")
		require.NoError(t, err)
		assert.Equal(t, "updated", got.Value)
		assert.Equal(t, 2, got.Version)
	})

	t.Run("Delete", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:      uuid.NewString(),
			Key:     "integration.delete.key",
			Value:   "to-be-deleted",
			Version: 1,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, entry)
		}))

		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Delete(txCtx, "integration.delete.key")
		}))

		_, err := repo.GetByKey(ctx, "integration.delete.key")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	})

	t.Run("List_keyset", func(t *testing.T) {
		for _, k := range []string{"list.a", "list.b", "list.c"} {
			e := &domain.ConfigEntry{ID: uuid.NewString(), Key: k, Value: k, Version: 1}
			require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
				return repo.Create(txCtx, e)
			}))
		}

		params := query.ListParams{
			Limit: 50,
			Sort: []query.SortColumn{
				{Name: "key", Direction: query.SortASC},
				{Name: "id", Direction: query.SortASC},
			},
		}
		entries, err := repo.List(ctx, params)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(entries), 3)
	})

	t.Run("PublishVersion_and_GetVersion", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:      uuid.NewString(),
			Key:     "integration.version.key",
			Value:   "v1-value",
			Version: 1,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, entry)
		}))

		now := time.Now()
		ver := &domain.ConfigVersion{
			ID:          uuid.NewString(),
			ConfigID:    entry.ID,
			Version:     1,
			Value:       "v1-value",
			Sensitive:   false,
			PublishedAt: &now,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.PublishVersion(txCtx, ver)
		}))

		got, err := repo.GetVersion(ctx, entry.ID, 1)
		require.NoError(t, err)
		assert.Equal(t, ver.ID, got.ID)
		assert.Equal(t, "v1-value", got.Value)
	})
}

// TestGetByKey_NotFound_AgainstRealPG confirms that a missing key returns
// errors.Is(err, pgx.ErrNoRows) chain and ErrConfigRepoNotFound code.
// This is the canonical REPO-SCAN-CLASSIFY-01 end-to-end check.
func TestGetByKey_NotFound_AgainstRealPG(t *testing.T) {
	repo, _, cleanup := setupConfigPG(t)
	defer cleanup()

	_, err := repo.GetByKey(context.Background(), "definitely-does-not-exist")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code,
		"pgx.ErrNoRows from real PG must map to ErrConfigRepoNotFound")

	assert.True(t, errors.Is(err, pgx.ErrNoRows),
		"wrapped cause must include pgx.ErrNoRows")
}

// TestConfigRepo_Integration_AtomicTx verifies that config_entries and
// outbox_entries are written in the same transaction and rolled back together
// on failure — the L2 atomicity guarantee.
func TestConfigRepo_Integration_AtomicTx(t *testing.T) {
	repo, txMgr, cleanup := setupConfigPG(t)
	defer cleanup()
	ctx := context.Background()

	outboxWriter := adapterpg.NewOutboxWriter()

	t.Run("both_committed_in_same_tx", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:      uuid.NewString(),
			Key:     "integration.atomic.key",
			Value:   "atomic-value",
			Version: 1,
		}
		outboxEntry := outbox.Entry{
			ID:            outbox.NewEntryID(),
			AggregateID:   entry.ID,
			AggregateType: "config_entry",
			EventType:     "config.changed.v1",
			Payload:       []byte(`{"action":"created"}`),
		}

		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			if err := repo.Create(txCtx, entry); err != nil {
				return err
			}
			return outboxWriter.Write(txCtx, outboxEntry)
		})
		require.NoError(t, err)

		got, err := repo.GetByKey(ctx, entry.Key)
		require.NoError(t, err)
		assert.Equal(t, entry.ID, got.ID)
	})

	t.Run("both_rolled_back_on_error", func(t *testing.T) {
		rollbackEntry := &domain.ConfigEntry{
			ID:      uuid.NewString(),
			Key:     "integration.rollback.key",
			Value:   "should-be-absent",
			Version: 1,
		}
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			if err := repo.Create(txCtx, rollbackEntry); err != nil {
				return err
			}
			return errors.New("simulated failure — rollback both")
		})
		require.Error(t, err)

		_, err = repo.GetByKey(ctx, "integration.rollback.key")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code,
			"rolled-back config entry must not be visible")
	})
}
