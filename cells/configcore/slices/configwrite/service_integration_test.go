//go:build integration

package configwrite

import (
	"context"
	"errors"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"log/slog"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	cctestutil "github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// adminIntegCtx returns a context carrying an admin principal for integration
// service-method calls.
func adminIntegCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
}

// writeBundle exposes the pool so tests can assert raw outbox_entries state
// — the L2 co-commit invariant can only be verified by querying outbox_entries
// directly, not via the domain repo.
type writeBundle struct {
	svc  *Service
	pool *pgxpool.Pool
}

// setupWriteService spins up a PostgreSQL container, applies migrations,
// and returns a Service wired with PG repo + outbox writer + tx manager.
func setupWriteService(t *testing.T) (writeBundle, func()) {
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

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())
	outboxWriter := adapterpg.NewOutboxWriter(clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, outboxWriter)),
		WithTxManager(txMgr),
	)

	cleanup := func() {
		_ = pool.Close(ctx)
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate container: %v", err)
		}
	}

	return writeBundle{svc: svc, pool: pool.DB()}, cleanup
}

// countOutboxRowsByEventType returns the number of outbox_entries rows for
// the given event_type. Used to assert the L2 domain + outbox co-commit.
func countOutboxRowsByEventType(t *testing.T, pool *pgxpool.Pool, eventType string) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox_entries WHERE event_type = $1`,
		eventType,
	).Scan(&count)
	require.NoError(t, err)
	return count
}

// TestCreate_AtomicWithOutbox verifies that config_entries and outbox_entries
// rows are both committed in the same transaction (L2 atomicity).
func TestCreate_AtomicWithOutbox(t *testing.T) {
	bundle, cleanup := setupWriteService(t)
	defer cleanup()

	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)
	require.Equal(t, 0, before, "baseline outbox count must be 0")

	entry, err := bundle.svc.Create(adminIntegCtx(), CreateInput{
		Key:   "integration.atomic.write",
		Value: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "integration.atomic.write", entry.Key)
	assert.Equal(t, 1, entry.Version)

	// Outbox-side: Create's L2 co-commit must have added exactly one
	// event.config.entry-upserted.v1 row, atomically with the config_entries row.
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)
	assert.Equal(t, 1, after-before,
		"Create must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigEntryUpserted)
}

// TestUpdate_AtomicWithOutbox verifies that the config_entries row is updated
// and an outbox_entries row is co-committed in the same transaction (L2 atomicity).
func TestUpdate_AtomicWithOutbox(t *testing.T) {
	bundle, cleanup := setupWriteService(t)
	defer cleanup()

	// Seed an entry via Create (which itself commits atomically).
	_, err := bundle.svc.Create(adminIntegCtx(), CreateInput{
		Key:   "integration.atomic.update",
		Value: "initial",
	})
	require.NoError(t, err)

	// Baseline: 1 outbox row from Create above.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)

	updated, err := bundle.svc.Update(adminIntegCtx(), UpdateInput{
		Key:   "integration.atomic.update",
		Value: "updated-value",
	})
	require.NoError(t, err)
	assert.Equal(t, "integration.atomic.update", updated.Key)
	assert.Equal(t, 2, updated.Version, "Update must bump version")

	// Outbox-side: Update's L2 co-commit must have added exactly one outbox row.
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)
	assert.Equal(t, 1, after-before,
		"Update must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigEntryUpserted)
}

// TestDelete_AtomicWithOutbox verifies that the config_entries row is deleted
// and an outbox_entries row is co-committed in the same transaction (L2 atomicity).
func TestDelete_AtomicWithOutbox(t *testing.T) {
	bundle, cleanup := setupWriteService(t)
	defer cleanup()

	// Seed an entry via Create.
	_, err := bundle.svc.Create(adminIntegCtx(), CreateInput{
		Key:   "integration.atomic.delete",
		Value: "to-be-deleted",
	})
	require.NoError(t, err)

	// Baseline: 1 outbox row from Create above.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryDeleted)

	err = bundle.svc.Delete(adminIntegCtx(), "integration.atomic.delete")
	require.NoError(t, err)

	// Outbox-side: Delete's L2 co-commit must have added exactly one outbox row.
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryDeleted)
	assert.Equal(t, 1, after-before,
		"Delete must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigEntryDeleted)

	// Domain-side: the config_entries row must be absent after Delete.
	_, getErr := bundle.svc.repo.GetByKey(context.Background(), "integration.atomic.delete")
	require.Error(t, getErr, "config_entries row must not exist after Delete")
	var ec *errcode.Error
	require.ErrorAs(t, getErr, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code,
		"deleted entry must return ErrConfigRepoNotFound")
}

// TestCreate_RollbackOnOutboxFailure verifies that when the outbox write
// returns a permanent error, the config_entries row is absent (transaction
// rolled back atomically).
func TestCreate_RollbackOnOutboxFailure(t *testing.T) {
	testutil.RequireDocker(t)
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
	defer func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
	}()

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())

	// Inject a writer that always fails — simulates outbox unavailable.
	failingWriter := &cctestutil.RecordingWriter{Err: errors.New("outbox broker down")}

	txMgr := adapterpg.NewTxManager(pool)
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, failingWriter)),
		WithTxManager(txMgr),
	)

	_, err = svc.Create(adminIntegCtx(), CreateInput{Key: "rollback.test", Value: "v"})
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
