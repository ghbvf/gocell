//go:build integration

package configpublish

import (
	"context"
	"errors"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"log/slog"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	cctestutil "github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// publishServiceBundle groups the PG-backed components for integration tests.
// pool and txMgr are exposed so tests can seed rows inside a tx (write path
// requires ambient tx per resolveWrite) and assert raw outbox_entries state.
type publishServiceBundle struct {
	svc   *Service
	repo  *cellpg.ConfigRepository
	pool  *pgxpool.Pool
	txMgr *adapterpg.TxManager
}

// setupPublishBundle spins up a PostgreSQL container, applies migrations,
// and returns a publish Service with PG repo + outbox writer + tx manager,
// plus a cleanup function.
func setupPublishBundle(t *testing.T) (publishServiceBundle, func()) {
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
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil)
	outboxWriter := adapterpg.NewOutboxWriter()
	txMgr := adapterpg.NewTxManager(pool)

	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, outboxWriter)),
		WithTxManager(txMgr),
	)

	cleanup := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return publishServiceBundle{svc: svc, repo: repo, pool: pool.DB(), txMgr: txMgr}, cleanup
}

// seedConfigEntry inserts a config_entries row through a real transaction.
// The write path requires an ambient pgx.Tx (persistence.TxCtxKey); seeding
// outside RunInTx would fail with ErrAdapterPGNoTx.
func seedConfigEntry(t *testing.T, b publishServiceBundle, key, value string) *domain.ConfigEntry {
	t.Helper()
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID:        uuid.NewString(),
		Key:       key,
		Value:     value,
		Sensitive: false,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, b.txMgr.RunInTx(context.Background(), func(txCtx context.Context) error {
		return b.repo.Create(txCtx, entry)
	}))
	return entry
}

// countOutboxRowsByEventType returns the number of rows in outbox_entries
// matching the given event_type. Used to assert the L2 domain + outbox
// co-commit invariant from raw SQL (not via the repo).
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

// TestPublishVersion_AtomicWithOutbox verifies that config_versions and
// outbox_entries rows are both committed in the same transaction (L2 atomicity).
// Uses a real PostgreSQL backend with migration 004 applied.
func TestPublishVersion_AtomicWithOutbox(t *testing.T) {
	bundle, cleanup := setupPublishBundle(t)
	defer cleanup()
	ctx := context.Background()

	entry := seedConfigEntry(t, bundle, "integration.publish.key", "publish-value")

	// Baseline: seed did NOT emit an outbox row (only Publish does). The
	// count before is 0 and must become 1 after Publish to prove the L2
	// co-commit on the same tx as the config_versions row.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigChanged)
	require.Equal(t, 0, before, "seed must not write to outbox_entries")

	ver, err := bundle.svc.Publish(ctx, "integration.publish.key")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)
	assert.NotNil(t, ver.PublishedAt)

	// Domain-side: the persisted version row confirms the repo write committed.
	got, err := bundle.repo.GetVersion(ctx, entry.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, ver.ID, got.ID)
	assert.Equal(t, "publish-value", got.Value)

	// Outbox-side: Publish's L2 co-commit must have added exactly one
	// event.config.version-published.v1 row to outbox_entries in the same tx.
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigChanged)
	assert.Equal(t, 1, after-before,
		"Publish must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigChanged)
}

// TestRollback_AtomicWithOutbox verifies that config_entries (version bump) and
// outbox_entries rows are both committed in the same transaction (L2 atomicity)
// during Rollback. Uses a real PostgreSQL backend.
func TestRollback_AtomicWithOutbox(t *testing.T) {
	bundle, cleanup := setupPublishBundle(t)
	defer cleanup()
	ctx := context.Background()

	// Seed an entry and publish a version so Rollback has a target.
	seedConfigEntry(t, bundle, "integration.rollback.key", "rollback-value")

	// Publish v1 to create a config_versions row to roll back to.
	ver, err := bundle.svc.Publish(ctx, "integration.rollback.key")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)

	// Baseline: count outbox rows after Publish (exactly 1 from Publish above).
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigRollback)
	require.Equal(t, 0, before, "no rollback outbox rows should exist before Rollback call")

	// Rollback to version 1.
	rolled, err := bundle.svc.Rollback(ctx, "integration.rollback.key", 1)
	require.NoError(t, err)
	assert.Equal(t, 2, rolled.Version,
		"Rollback must increment the config_entries version (UPDATE...RETURNING)")

	// Outbox-side: Rollback's L2 co-commit must have added exactly one
	// event.config.rollback row to outbox_entries in the same tx.
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigRollback)
	assert.Equal(t, 1, after-before,
		"Rollback must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigRollback)
}

// TestRollback_AtomicWithOutbox_FailureRollsBackBoth verifies that when the outbox
// write fails during Rollback, both the config_entries update and the outbox write
// are rolled back (transaction atomicity).
func TestRollback_AtomicWithOutbox_FailureRollsBackBoth(t *testing.T) {
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

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil)
	txMgr := adapterpg.NewTxManager(pool)

	// First: seed and publish using a good writer.
	goodWriter := adapterpg.NewOutboxWriter()
	svcGood := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, goodWriter)),
		WithTxManager(txMgr),
	)

	b := publishServiceBundle{svc: svcGood, repo: repo, pool: pool.DB(), txMgr: txMgr}
	seedConfigEntry(t, b, "rollback.failure.key", "initial-value")
	_, err = svcGood.Publish(ctx, "rollback.failure.key")
	require.NoError(t, err)

	// Capture the config_entries version before the failing Rollback.
	entryBefore, err := repo.GetByKey(ctx, "rollback.failure.key")
	require.NoError(t, err)
	versionBefore := entryBefore.Version

	// Now inject a failing writer — simulates outbox broker down during Rollback.
	failingWriter := &cctestutil.RecordingWriter{Err: errors.New("outbox broker down")}
	svcFail := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, failingWriter)),
		WithTxManager(txMgr),
	)

	_, err = svcFail.Rollback(ctx, "rollback.failure.key", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox")

	// config_entries version must NOT have changed (rolled back).
	entryAfter, err := repo.GetByKey(ctx, "rollback.failure.key")
	require.NoError(t, err)
	assert.Equal(t, versionBefore, entryAfter.Version,
		"config_entries version must not change when outbox write fails (atomic rollback)")
}
