//go:build integration

package configwrite

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/internal/testoutbox"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"

	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	cctestutil "github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/crypto"
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

// setupWriteService clones the package-shared pre-migrated template DB
// into a fresh per-test database and returns a Service wired with PG repo
// + outbox writer + tx manager. Pool + per-test DB lifecycle is
// owned by t.Cleanup inside sharedPG.NewPerTestPool (see testmain_integration_test.go).
func setupWriteService(t *testing.T) writeBundle {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)
	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())
	outboxWriter := adapterpg.NewOutboxWriter(clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, outboxWriter))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)

	return writeBundle{svc: svc, pool: pool.DB()}
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
	bundle := setupWriteService(t)

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
	bundle := setupWriteService(t)

	// Seed an entry via Create (which itself commits atomically).
	_, err := bundle.svc.Create(adminIntegCtx(), CreateInput{
		Key:   "integration.atomic.update",
		Value: "initial",
	})
	require.NoError(t, err)

	// Baseline: 1 outbox row from Create above.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)

	updated, err := bundle.svc.Update(adminIntegCtx(), UpdateInput{
		Key:             "integration.atomic.update",
		Value:           "updated-value",
		ExpectedVersion: 1,
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
	bundle := setupWriteService(t)

	// Seed an entry via Create.
	_, err := bundle.svc.Create(adminIntegCtx(), CreateInput{
		Key:   "integration.atomic.delete",
		Value: "to-be-deleted",
	})
	require.NoError(t, err)

	// Baseline: 1 outbox row from Create above.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryDeleted)

	err = bundle.svc.Delete(adminIntegCtx(), "integration.atomic.delete", 1)
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

// TestL2Atomicity_configwrite_RollsBack verifies that when the outbox write
// returns a permanent error on Create, the config_entries row is absent
// (transaction rolled back atomically — L2 canonical rollback proof).
//
// Negative control: after asserting the rollback, a second Service with a
// pass-through writer performs the same Create and asserts the row persists.
// This guards against a vacuous-pass scenario where the test setup itself is
// broken (e.g., the key was never written in the first place).
func TestL2Atomicity_configwrite_RollsBack(t *testing.T) {
	ctx := context.Background()
	pool := sharedPG.NewPerTestPool(t)

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	// Inject a writer that always fails — simulates outbox unavailable.
	failingWriter := &cctestutil.RecordingWriter{Err: errors.New("outbox broker down")}

	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failingWriter))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)

	_, err = svc.Create(adminIntegCtx(), CreateInput{Key: "rollback.test", Value: "v"})
	require.Error(t, err)
	// Exact-sentinel check: the error must wrap the injected sentinel, not a
	// coincidental substring match from unrelated infrastructure. This also
	// proves Write was actually invoked — the sentinel instance only exists
	// inside the writer, so a wiring bug that skipped Write could not produce it.
	assert.ErrorIs(t, err, failingWriter.Err,
		"Create error must wrap the injected outbox sentinel (proves Write was invoked)")

	// config_entries row must NOT exist (rolled back).
	_, getErr := repo.GetByKey(ctx, "rollback.test")
	require.Error(t, getErr)
	var ec *errcode.Error
	require.ErrorAs(t, getErr, &ec)
	assert.Equal(t, errcode.ErrConfigRepoNotFound, ec.Code,
		"config entry must not persist after outbox-failure rollback")

	// Negative control: pass-through Service on the same pool/txMgr must
	// succeed, proving the rollback assertion above is not vacuously trivial
	// (i.e., Create genuinely writes a row on the happy path).
	passSvc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, adapterpg.NewOutboxWriter(clock.Real())))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)
	got, err := passSvc.Create(adminIntegCtx(), CreateInput{Key: "rollback.test", Value: "v"})
	require.NoError(t, err, "negative control: Create must succeed with pass-through writer")
	assert.Equal(t, "rollback.test", got.Key)
	assert.Equal(t, "v", got.Value)

	controlEntry, getErr := repo.GetByKey(ctx, "rollback.test")
	require.NoError(t, getErr, "negative control: config_entries row must exist after successful Create")
	assert.Equal(t, "v", controlEntry.Value,
		"negative control: persisted value must match the Create input")
}

// TestL2Atomicity_configwrite_RollsBack_Update verifies that when the outbox
// write fails during Update, the config_entries row stays at the pre-update
// version (Update rolled back atomically — per-mutation L2 rollback proof).
func TestL2Atomicity_configwrite_RollsBack_Update(t *testing.T) {
	ctx := context.Background()
	pool := sharedPG.NewPerTestPool(t)
	txMgr := adapterpg.NewTxManager(pool)

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())

	// Phase 1: seed the entry with a pass-through writer so the Create commits.
	passSvc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, adapterpg.NewOutboxWriter(clock.Real())))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)
	_, err = passSvc.Create(adminIntegCtx(), CreateInput{Key: "rollback.update.key", Value: "initial"})
	require.NoError(t, err)

	// Verify the seed committed at version 1.
	seedEntry, err := repo.GetByKey(ctx, "rollback.update.key")
	require.NoError(t, err)
	require.Equal(t, 1, seedEntry.Version, "seed must commit at version 1")

	// Phase 2: construct a second Service against the SAME pool/txMgr but with
	// a failing writer. Update must error and the row must stay at version 1.
	failingWriter := &cctestutil.RecordingWriter{Err: errors.New("outbox broker down")}
	failSvc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failingWriter))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)

	_, err = failSvc.Update(adminIntegCtx(), UpdateInput{
		Key:             "rollback.update.key",
		Value:           "should-not-persist",
		ExpectedVersion: 1,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, failingWriter.Err,
		"Update error must wrap the injected outbox sentinel (proves Write was invoked)")

	// config_entries must still be at version 1 (UPDATE rolled back).
	afterEntry, getErr := repo.GetByKey(ctx, "rollback.update.key")
	require.NoError(t, getErr)
	assert.Equal(t, 1, afterEntry.Version,
		"config_entries version must not change when outbox write fails (atomic Update rollback)")
	assert.Equal(t, "initial", afterEntry.Value,
		"config_entries value must not change when outbox write fails (atomic Update rollback)")
}

// TestL2Atomicity_configwrite_RollsBack_Delete verifies that when the outbox
// write fails during Delete, the config_entries row is still present
// (Delete rolled back atomically — per-mutation L2 rollback proof).
func TestL2Atomicity_configwrite_RollsBack_Delete(t *testing.T) {
	ctx := context.Background()
	pool := sharedPG.NewPerTestPool(t)
	txMgr := adapterpg.NewTxManager(pool)

	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, crypto.NoopTransformer{}, nil, clock.Real())

	// Phase 1: seed the entry with a pass-through writer so the Create commits.
	passSvc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, adapterpg.NewOutboxWriter(clock.Real())))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)
	_, err = passSvc.Create(adminIntegCtx(), CreateInput{Key: "rollback.delete.key", Value: "to-be-deleted"})
	require.NoError(t, err)

	// Verify the seed committed.
	_, err = repo.GetByKey(ctx, "rollback.delete.key")
	require.NoError(t, err, "seed entry must exist before the failing Delete")

	// Phase 2: failing-writer Service — Delete must error and the row must survive.
	failingWriter := &cctestutil.RecordingWriter{Err: errors.New("outbox broker down")}
	failSvc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failingWriter))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)

	deleteErr := failSvc.Delete(adminIntegCtx(), "rollback.delete.key", 1)
	require.Error(t, deleteErr)
	assert.ErrorIs(t, deleteErr, failingWriter.Err,
		"Delete error must wrap the injected outbox sentinel (proves Write was invoked)")

	// config_entries row must still exist (DELETE rolled back).
	afterEntry, getErr := repo.GetByKey(ctx, "rollback.delete.key")
	require.NoError(t, getErr,
		"config_entries row must still exist when outbox write fails (atomic Delete rollback)")
	assert.Equal(t, "to-be-deleted", afterEntry.Value,
		"config_entries value must be unchanged when outbox write fails (atomic Delete rollback)")
}
