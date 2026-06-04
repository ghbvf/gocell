//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// integrationTestTenantA / integrationTestTenantB are canonical test-tenant
// UUIDs used in integration tests for cross-tenant isolation assertions.
var (
	integrationTestTenantA = mustTenant("00000000-0000-0000-0000-000000000001")
	integrationTestTenantB = mustTenant("00000000-0000-0000-0000-000000000002")
)

// setupConfigPG clones the package-shared pre-migrated template database
// into a fresh per-test database and returns a ConfigRepository wired
// over it. Pool + per-test DB lifecycle is owned by t.Cleanup registered
// inside sharedPG.NewPerTestPool (see testmain_integration_test.go).
func setupConfigPG(t *testing.T) (*ConfigRepository, *adapterpg.TxManager) {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)
	session := NewSession(pool.DB())
	repo := NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	return repo, txMgr
}

// TestConfigRepo_Integration_CRUD exercises all 7 repository methods
// (Create / GetByKey / Update / Delete / List / PublishVersion / GetVersion)
// against a real PostgreSQL instance with migration 004 applied.
func TestConfigRepo_Integration_CRUD(t *testing.T) {
	repo, txMgr := setupConfigPG(t)
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
			return repo.Create(txCtx, integrationTestTenantA, entry)
		}))

		got, err := repo.GetByKey(ctx, integrationTestTenantA, "integration.test.key")
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
			return repo.Create(txCtx, integrationTestTenantA, entry)
		}))

		var updated *domain.ConfigEntry
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			var err error
			updated, err = repo.Update(txCtx, integrationTestTenantA, "integration.update.key", 1, "updated")
			return err
		}))

		require.NotNil(t, updated)
		assert.Equal(t, "updated", updated.Value)
		assert.Equal(t, 2, updated.Version)

		got, err := repo.GetByKey(ctx, integrationTestTenantA, "integration.update.key")
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
			return repo.Create(txCtx, integrationTestTenantA, entry)
		}))

		var deleted *domain.ConfigEntry
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			var err error
			deleted, err = repo.Delete(txCtx, integrationTestTenantA, "integration.delete.key", 1)
			return err
		}))
		require.NotNil(t, deleted)
		assert.Equal(t, "to-be-deleted", deleted.Value)

		_, err := repo.GetByKey(ctx, integrationTestTenantA, "integration.delete.key")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	})

	t.Run("List_keyset", func(t *testing.T) {
		for _, k := range []string{"list.a", "list.b", "list.c"} {
			e := &domain.ConfigEntry{ID: uuid.NewString(), Key: k, Value: k, Version: 1}
			require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
				return repo.Create(txCtx, integrationTestTenantA, e)
			}))
		}

		params := query.ListParams{
			Limit: 50,
			Sort: []query.SortColumn{
				{Name: "key", Direction: query.SortASC},
				{Name: "id", Direction: query.SortASC},
			},
		}
		entries, err := repo.List(ctx, integrationTestTenantA, params)
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
			return repo.Create(txCtx, integrationTestTenantA, entry)
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
			return repo.PublishVersion(txCtx, integrationTestTenantA, ver)
		}))

		got, err := repo.GetVersion(ctx, integrationTestTenantA, entry.ID, 1)
		require.NoError(t, err)
		assert.Equal(t, ver.ID, got.ID)
		assert.Equal(t, "v1-value", got.Value)
	})
}

// TestGetByKey_NotFound_AgainstRealPG confirms that a missing key returns
// errors.Is(err, pgx.ErrNoRows) chain and ErrConfigRepoNotFound code.
// This is the canonical REPO-SCAN-CLASSIFY-01 end-to-end check.
func TestGetByKey_NotFound_AgainstRealPG(t *testing.T) {
	repo, _ := setupConfigPG(t)

	_, err := repo.GetByKey(context.Background(), integrationTestTenantA, "definitely-does-not-exist")
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
	repo, txMgr := setupConfigPG(t)
	ctx := context.Background()

	outboxWriter := adapterpg.NewOutboxWriter(clock.Real())

	t.Run("both_committed_in_same_tx", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:      uuid.NewString(),
			Key:     "integration.atomic.key",
			Value:   "atomic-value",
			Version: 1,
		}
		outboxEntry, err := outbox.NewEntry(clock.Real(), ctx, domain.TopicConfigEntryUpserted,
			[]byte(`{"key":"integration.atomic.key","value":"atomic-value","version":1}`),
			outbox.WithAggregateID(entry.ID),
			outbox.WithAggregateType("config_entry"),
		)
		require.NoError(t, err)

		err = txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			if err := repo.Create(txCtx, integrationTestTenantA, entry); err != nil {
				return err
			}
			return outboxWriter.Write(txCtx, outboxEntry)
		})
		require.NoError(t, err)

		got, err := repo.GetByKey(ctx, integrationTestTenantA, entry.Key)
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
			if err := repo.Create(txCtx, integrationTestTenantA, rollbackEntry); err != nil {
				return err
			}
			return errors.New("simulated failure — rollback both")
		})
		require.Error(t, err)

		_, err = repo.GetByKey(ctx, integrationTestTenantA, "integration.rollback.key")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code,
			"rolled-back config entry must not be visible")
	})
}

// setupConfigPGEncrypted spins up a PG container identical to setupConfigPG
// but wires the ConfigRepository with a real LocalAESKeyProvider (envelope
// AES-GCM with AAD binding) so sensitive-value tests exercise the full
// encrypt → BYTEA persist → decrypt path end-to-end rather than the mock
// unit-test shortcut.
func setupConfigPGEncrypted(t *testing.T) (*ConfigRepository, *adapterpg.TxManager) {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)

	// Real LocalAES master key (32-byte hex, deterministic for reproducibility).
	kp, err := crypto.NewLocalAESKeyProviderFromKeys(
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "")
	require.NoError(t, err)
	transformer := crypto.NewValueTransformer(kp)

	session := NewSession(pool.DB())
	repo := NewConfigRepository(session, transformer, nil, clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	return repo, txMgr
}

// TestConfigRepo_Integration_Encryption_RoundTrip verifies the full
// encrypt → BYTEA persist → decrypt path against real PostgreSQL using
// LocalAESKeyProvider (envelope encryption, AES-GCM with AAD binding).
// This complements the unit-test mocks in config_repo_encrypt_test.go by
// exercising BYTEA column ordering, NULL handling, and base64 key decoding
// end-to-end in the same way production code runs.
func TestConfigRepo_Integration_Encryption_RoundTrip(t *testing.T) {
	repo, txMgr := setupConfigPGEncrypted(t)
	ctx := context.Background()

	t.Run("create_sensitive_then_read_returns_plaintext", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:        uuid.NewString(),
			Key:       "integration.encrypted.db_password",
			Value:     "s3cret-production-value",
			Sensitive: true,
			Version:   1,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, integrationTestTenantA, entry)
		}))

		got, err := repo.GetByKey(ctx, integrationTestTenantA, entry.Key)
		require.NoError(t, err, "sensitive entry must decrypt cleanly on read")
		assert.Equal(t, "s3cret-production-value", got.Value,
			"round-trip: plaintext must survive encrypt → BYTEA → decrypt")
		assert.True(t, got.Sensitive)
		assert.NotEmpty(t, got.KeyID, "stored row must carry key_id for staleness detection")
		assert.False(t, got.Stale, "current-key write must not appear stale")
	})

	t.Run("update_sensitive_re_encrypts_and_reads_back", func(t *testing.T) {
		entry := &domain.ConfigEntry{
			ID:        uuid.NewString(),
			Key:       "integration.encrypted.api_token",
			Value:     "initial-token",
			Sensitive: true,
			Version:   1,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, integrationTestTenantA, entry)
		}))

		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.UpdateForRollback(txCtx, integrationTestTenantA, entry.Key, 1, "rotated-token", true)
			return err
		}))

		got, err := repo.GetByKey(ctx, integrationTestTenantA, entry.Key)
		require.NoError(t, err)
		assert.Equal(t, "rotated-token", got.Value)
		assert.Equal(t, 2, got.Version)
	})
}

// dropBothProbeTablesSQL / dropFeatureFlagsOnlySQL drive setupBrokenConfigPG.
// The both-tables form exercises the config_entries Exec branch (first probe
// fails); the feature_flags-only form exercises the feature_flags Exec branch
// (first probe succeeds, second fails) so RepoReady's two failure branches are
// each covered by a real PG failure injection.
const (
	dropBothProbeTablesSQL  = "DROP TABLE config_entries CASCADE; DROP TABLE feature_flags CASCADE"
	dropFeatureFlagsOnlySQL = "DROP TABLE feature_flags CASCADE"
)

// setupBrokenConfigPG spins up a PostgreSQL container, applies all migrations,
// runs dropSQL to simulate schema drift, and returns a ConfigRepository backed
// by that broken database. The returned repo is used as the "broken" prober in
// RunRepoReadinessConformance / targeted branch tests.
func setupBrokenConfigPG(t *testing.T, dropSQL string) *ConfigRepository {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)

	// Drop table(s) to simulate schema drift / missing migration. Per-test
	// DB isolation (TEMPLATE clone) ensures the DROP cannot leak across
	// tests — each broken-prober invocation operates on its own database.
	ctx := context.Background()
	_, dropErr := pool.DB().Exec(ctx, dropSQL)
	require.NoError(t, dropErr, "dropping tables to create broken repo")

	session := NewSession(pool.DB())
	return NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())
}

// TestConfigRepo_Integration_RepoReadiness exercises the differentiated repo
// readiness probe against a real PostgreSQL instance.
// healthy: full migrations applied — probe must return nil.
// broken: config_entries and feature_flags dropped — probe must return an error.
func TestConfigRepo_Integration_RepoReadiness(t *testing.T) {
	healthy, _ := setupConfigPG(t)
	broken := setupBrokenConfigPG(t, dropBothProbeTablesSQL)

	celltest.RunRepoReadinessConformance(t, "configcore-pg", healthy, broken)
}

// TestConfigRepo_Integration_RepoReadiness_FeatureFlagsDrop covers RepoReady's
// second failure branch: with config_entries intact but feature_flags dropped,
// the first Exec probe succeeds and the second must fail. The both-tables
// broken prober above only reaches the first branch (returns on config_entries
// error), so this targeted injection is required for full branch coverage of
// the differentiated probe.
func TestConfigRepo_Integration_RepoReadiness_FeatureFlagsDrop(t *testing.T) {
	repo := setupBrokenConfigPG(t, dropFeatureFlagsOnlySQL)

	err := repo.RepoReady(context.Background())
	require.Error(t, err, "feature_flags dropped — RepoReady must surface the second-probe failure")
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "RepoReady error must be an *errcode.Error")
	require.Equal(t, errcode.ErrConfigRepoQuery, ec.Code,
		"second-probe failure must carry ErrConfigRepoQuery")
}

// TestConfigRepo_Integration_CrossTenantIsolation verifies that config entries
// written under testTenantA are invisible to testTenantB on every data method:
// GetByKey/Update/Delete/List/GetVersion all return not-found or empty under
// the wrong tenant. Same-tenant access must still succeed.
//
// This mirrors accesscore's conformCrossTenantIsolation pattern.
func TestConfigRepo_Integration_CrossTenantIsolation(t *testing.T) {
	repo, txMgr := setupConfigPG(t)
	ctx := context.Background()

	key := "cross-tenant-isolation-" + uuid.NewString()
	entryID := uuid.NewString()
	entry := &domain.ConfigEntry{
		ID:      entryID,
		Key:     key,
		Value:   "tenant-a-only",
		Version: 1,
	}

	// Write under tenant A.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, integrationTestTenantA, entry)
	}))

	t.Run("GetByKey_under_tenantB_returns_not_found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, integrationTestTenantB, key)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	})

	t.Run("GetByKey_under_tenantA_succeeds", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, integrationTestTenantA, key)
		require.NoError(t, err)
		assert.Equal(t, "tenant-a-only", got.Value)
	})

	t.Run("Update_under_tenantB_returns_not_found", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Update(txCtx, integrationTestTenantB, key, 1, "should-not-apply")
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	})

	t.Run("Delete_under_tenantB_returns_not_found", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Delete(txCtx, integrationTestTenantB, key, 1)
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	})

	t.Run("List_under_tenantB_returns_empty", func(t *testing.T) {
		entries, err := repo.List(ctx, integrationTestTenantB, query.ListParams{
			Limit: 50,
			Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}},
		})
		require.NoError(t, err)
		for _, e := range entries {
			assert.NotEqual(t, key, e.Key, "tenant B must not see tenant A's entry in List")
		}
	})

	t.Run("GetVersion_under_tenantB_returns_not_found", func(t *testing.T) {
		// Publish a version under tenant A first.
		now := time.Now()
		ver := &domain.ConfigVersion{
			ID:          uuid.NewString(),
			ConfigID:    entryID,
			Version:     1,
			Value:       "tenant-a-only",
			Sensitive:   false,
			PublishedAt: &now,
		}
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.PublishVersion(txCtx, integrationTestTenantA, ver)
		}))

		_, err := repo.GetVersion(ctx, integrationTestTenantB, entryID, 1)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code)
	})

	t.Run("GetVersion_under_tenantA_succeeds", func(t *testing.T) {
		_, err := repo.GetVersion(ctx, integrationTestTenantA, entryID, 1)
		require.NoError(t, err)
	})
}

// TestConfigRepo_Integration_EmptyTenantGuard verifies that every data method
// rejects a zero/empty tenant.TenantID with a validation error rather than
// silently issuing a tenant-less query. This ensures the Validate() call in
// each production method is exercised end-to-end.
func TestConfigRepo_Integration_EmptyTenantGuard(t *testing.T) {
	repo, txMgr := setupConfigPG(t)
	ctx := context.Background()
	zero := tenant.TenantID("") // intentionally invalid

	assertValidationErr := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "empty tenant must return error")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
			"empty tenant must return ErrValidationFailed, not a DB error")
	}

	t.Run("Create_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, zero, &domain.ConfigEntry{Key: "k"})
		})
		assertValidationErr(t, err)
	})

	t.Run("GetByKey_zero_tenant", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, zero, "k")
		assertValidationErr(t, err)
	})

	t.Run("Update_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Update(txCtx, zero, "k", 1, "v")
			return err
		})
		assertValidationErr(t, err)
	})

	t.Run("UpdateForRollback_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.UpdateForRollback(txCtx, zero, "k", 1, "v", false)
			return err
		})
		assertValidationErr(t, err)
	})

	t.Run("Delete_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Delete(txCtx, zero, "k", 1)
			return err
		})
		assertValidationErr(t, err)
	})

	t.Run("List_zero_tenant", func(t *testing.T) {
		_, err := repo.List(ctx, zero, query.ListParams{Limit: 10})
		assertValidationErr(t, err)
	})

	t.Run("PublishVersion_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.PublishVersion(txCtx, zero, &domain.ConfigVersion{ConfigID: "cfg-1"})
		})
		assertValidationErr(t, err)
	})

	t.Run("GetVersion_zero_tenant", func(t *testing.T) {
		_, err := repo.GetVersion(ctx, zero, "cfg-1", 1)
		assertValidationErr(t, err)
	})
}
