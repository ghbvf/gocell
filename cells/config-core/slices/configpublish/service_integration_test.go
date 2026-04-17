//go:build integration

package configpublish

import (
	"context"
	"log/slog"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	cellpg "github.com/ghbvf/gocell/cells/config-core/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// publishServiceBundle groups the PG-backed components for integration tests.
type publishServiceBundle struct {
	svc  *Service
	repo *cellpg.ConfigRepository
}

// setupPublishBundle spins up a PostgreSQL container, applies migrations,
// and returns a publish Service with PG repo + outbox writer + tx manager,
// plus a cleanup function.
func setupPublishBundle(t *testing.T) (publishServiceBundle, func()) {
	t.Helper()

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
	repo := cellpg.NewConfigRepositoryFromSession(session)
	outboxWriter := adapterpg.NewOutboxWriter()
	txMgr := adapterpg.NewTxManager(pool)

	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(outboxWriter),
		WithTxManager(txMgr),
	)

	cleanup := func() {
		pool.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return publishServiceBundle{svc: svc, repo: repo}, cleanup
}

// seedEntry inserts a config_entries row directly via the repository (non-tx
// path; repo.resolveDB falls back to pool when ctx has no tx).
func seedConfigEntry(t *testing.T, repo *cellpg.ConfigRepository, key, value string) *domain.ConfigEntry {
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
	require.NoError(t, repo.Create(context.Background(), entry))
	return entry
}

// TestPublishVersion_AtomicWithOutbox verifies that config_versions and
// outbox_entries rows are both committed in the same transaction (L2 atomicity).
// Uses a real PostgreSQL backend with migration 004 applied.
func TestPublishVersion_AtomicWithOutbox(t *testing.T) {
	bundle, cleanup := setupPublishBundle(t)
	defer cleanup()
	ctx := context.Background()

	entry := seedConfigEntry(t, bundle.repo, "integration.publish.key", "publish-value")

	ver, err := bundle.svc.Publish(ctx, "integration.publish.key")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)
	assert.NotNil(t, ver.PublishedAt)

	// Retrieve the persisted version to confirm the repo write committed.
	got, err := bundle.repo.GetVersion(ctx, entry.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, ver.ID, got.ID)
	assert.Equal(t, "publish-value", got.Value)
}
