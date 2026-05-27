//go:build integration

package configpublish

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// adminIntegCtx returns a context carrying an admin principal for integration
// service-method calls. Repository calls don't need auth context.
func adminIntegCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
}

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
// and returns a publish Service with PG repo + outbox writer + tx manager.
// Uses NoopTransformer (sensitive=false only).
func setupPublishBundle(t *testing.T) publishServiceBundle {
	return setupPublishBundleWithTransformer(t, crypto.NoopTransformer{})
}

// setupPublishBundleEncrypted is the sibling of setupPublishBundle wired with
// a real LocalAES ValueTransformer so tests can exercise the sensitive=true
// SQL branch end-to-end (Create + Publish + Rollback round-trip through
// encrypt/decrypt). Uses a deterministic 32-byte hex master key for
// reproducibility, matching the pattern in config_repo_integration_test.go.
func setupPublishBundleEncrypted(t *testing.T) publishServiceBundle {
	kp, err := crypto.NewLocalAESKeyProviderFromKeys(
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "",
	)
	require.NoError(t, err)
	return setupPublishBundleWithTransformer(t, crypto.NewValueTransformer(kp))
}

// setupPublishBundleWithTransformer is the shared body of the two factories;
// callers pick the transformer that matches their test's sensitivity needs.
// Pool + per-test DB lifecycle is owned by t.Cleanup inside
// sharedPG.NewPerTestPool (see testmain_integration_test.go).
func setupPublishBundleWithTransformer(t *testing.T, transformer crypto.ValueTransformer) publishServiceBundle {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)
	session := cellpg.NewSession(pool.DB())
	repo := cellpg.NewConfigRepository(session, transformer, nil, clock.Real())
	outboxWriter := adapterpg.NewOutboxWriter(clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	svc, err := NewService(
		clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, outboxWriter))),
		WithTxManager(persistence.WrapForCell(txMgr)),
	)
	require.NoError(t, err)

	return publishServiceBundle{svc: svc, repo: repo, pool: pool.DB(), txMgr: txMgr}
}

// seedConfigEntry inserts a non-sensitive config_entries row through a real
// transaction. The write path requires an ambient pgx.Tx
// (persistence.TxCtxKey); seeding outside RunInTx would fail with
// ErrAdapterPGNoTx.
func seedConfigEntry(t *testing.T, b publishServiceBundle, key, value string) *domain.ConfigEntry {
	return seedConfigEntryWithSensitivity(t, b, key, value, false)
}

// seedConfigEntryWithSensitivity is the sensitivity-aware variant of
// seedConfigEntry; used by tests that need to exercise the
// sensitive=true branch of doUpdate.
func seedConfigEntryWithSensitivity(t *testing.T, b publishServiceBundle, key, value string, sensitive bool) *domain.ConfigEntry {
	t.Helper()
	now := time.Now()
	entry := &domain.ConfigEntry{
		ID:        uuid.NewString(),
		Key:       key,
		Value:     value,
		Sensitive: sensitive,
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
	err := pool.QueryRow(
		context.Background(),
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
	bundle := setupPublishBundle(t)
	repoCtx := context.Background()
	svcCtx := adminIntegCtx()

	entry := seedConfigEntry(t, bundle, "integration.publish.key", "publish-value")

	// Baseline: seed did NOT emit an outbox row (only Publish does). The
	// count before is 0 and must become 1 after Publish to prove the L2
	// co-commit on the same tx as the config_versions row.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigVersionPublished)
	require.Equal(t, 0, before, "seed must not write to outbox_entries")

	ver, err := bundle.svc.Publish(svcCtx, "integration.publish.key")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)
	assert.NotNil(t, ver.PublishedAt)

	// Domain-side: the persisted version row confirms the repo write committed.
	got, err := bundle.repo.GetVersion(repoCtx, entry.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, ver.ID, got.ID)
	assert.Equal(t, "publish-value", got.Value)

	// Outbox-side: Publish's L2 co-commit must have added exactly one
	// event.config.version-published.v1 row to outbox_entries in the same tx.
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigVersionPublished)
	assert.Equal(t, 1, after-before,
		"Publish must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigVersionPublished)
}

// TestRollback_AtomicWithOutbox verifies that config_entries (version bump) and
// outbox_entries rows are both committed in the same transaction (L2 atomicity)
// during Rollback. Uses a real PostgreSQL backend.
func TestRollback_AtomicWithOutbox(t *testing.T) {
	bundle := setupPublishBundle(t)
	svcCtx := adminIntegCtx()

	// Seed an entry and publish a version so Rollback has a target.
	seedConfigEntry(t, bundle, "integration.rollback.key", "rollback-value")

	// Publish v1 to create a config_versions row to roll back to.
	ver, err := bundle.svc.Publish(svcCtx, "integration.rollback.key")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)

	// Baseline: count outbox rows after Publish. Publish emits only
	// version-published, so Rollback-owned topics should still be absent.
	beforeState := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)
	beforeAudit := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigRollback)
	require.Equal(t, 0, beforeState, "no entry-upserted outbox rows should exist before Rollback call")
	require.Equal(t, 0, beforeAudit, "no rollback outbox rows should exist before Rollback call")

	// Rollback to version 1. The live entry is at version 1 (Publish does not
	// bump the live entry's version), so expectedVersion=1.
	rolled, err := bundle.svc.Rollback(svcCtx, "integration.rollback.key", 1, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, rolled.Version,
		"Rollback must increment the config_entries version (UPDATE...RETURNING)")

	// Outbox-side: Rollback's L2 co-commit must add state-sync and audit rows
	// to outbox_entries in the same tx.
	afterState := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)
	assert.Equal(t, 1, afterState-beforeState,
		"Rollback must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigEntryUpserted)

	afterAudit := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigRollback)
	assert.Equal(t, 1, afterAudit-beforeAudit,
		"Rollback must co-commit exactly one %s outbox row (L2 atomicity)", domain.TopicConfigRollback)
}

// TestL2Atomicity_configpublish_RollsBack verifies that when the outbox write
// fails during Rollback, both the config_entries update and the outbox write
// are rolled back (transaction atomicity — L2 canonical rollback proof).
func TestL2Atomicity_configpublish_RollsBack(t *testing.T) {
	bundle := setupPublishBundle(t)
	ctx := context.Background()
	svcCtx := adminIntegCtx()

	// First: seed and publish using the default good writer wired into bundle.svc.
	seedConfigEntry(t, bundle, "rollback.failure.key", "initial-value")
	_, err := bundle.svc.Publish(svcCtx, "rollback.failure.key")
	require.NoError(t, err)

	// Capture the config_entries version before the failing Rollback.
	entryBefore, err := bundle.repo.GetByKey(ctx, "rollback.failure.key")
	require.NoError(t, err)
	versionBefore := entryBefore.Version
	beforeState := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted)
	beforeAudit := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigRollback)

	// Inject a writer that succeeds on the state-sync row and fails on the
	// rollback audit row, sharing the bundle's repo/txMgr. The transaction
	// must roll both the config update and the first outbox row back.
	failingWriter := &failOnWriteNumberWriter{
		delegate: adapterpg.NewOutboxWriter(clock.Real()),
		failOn:   2,
		err:      errors.New("outbox broker down"),
	}
	svcFail, err := NewService(
		clock.Real(), bundle.repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failingWriter))),
		WithTxManager(persistence.WrapForCell(bundle.txMgr)),
	)
	require.NoError(t, err)

	_, err = svcFail.Rollback(svcCtx, "rollback.failure.key", 1, versionBefore)
	require.Error(t, err)
	assert.ErrorIs(t, err, failingWriter.err,
		"Rollback error must wrap the injected outbox sentinel")

	// config_entries version must NOT have changed (rolled back).
	entryAfter, err := bundle.repo.GetByKey(ctx, "rollback.failure.key")
	require.NoError(t, err)
	assert.Equal(t, versionBefore, entryAfter.Version,
		"config_entries version must not change when outbox write fails (atomic rollback)")
	assert.Equal(t, beforeState, countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigEntryUpserted),
		"entry-upserted outbox row must roll back when the later rollback audit write fails")
	assert.Equal(t, beforeAudit, countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigRollback),
		"rollback audit outbox row must not be committed after writer failure")

	// Negative control: pass-through Service on the same pool/txMgr must
	// succeed and increment the version. This guards against a vacuous-pass
	// where the prior version-unchanged assertion would hold even if Rollback
	// were a no-op, by proving Rollback genuinely bumps the version when the
	// outbox write succeeds.
	passBundle := setupPublishBundle(t)
	// Re-seed and publish on the pass-through bundle to get a valid Rollback target.
	seedConfigEntry(t, passBundle, "rollback.failure.control.key", "control-value")
	_, err = passBundle.svc.Publish(svcCtx, "rollback.failure.control.key")
	require.NoError(t, err)
	controlBefore, err := passBundle.repo.GetByKey(ctx, "rollback.failure.control.key")
	require.NoError(t, err)
	_, err = passBundle.svc.Rollback(svcCtx, "rollback.failure.control.key", 1, controlBefore.Version)
	require.NoError(t, err, "negative control: Rollback must succeed with pass-through writer")
	controlAfter, err := passBundle.repo.GetByKey(ctx, "rollback.failure.control.key")
	require.NoError(t, err)
	assert.Equal(t, controlBefore.Version+1, controlAfter.Version,
		"negative control: Rollback must increment config_entries version on success")
}

// TestL2Atomicity_configpublish_RollsBack_Publish verifies that when the outbox
// write fails during Publish, both the config_versions row and the outbox write
// are rolled back (transaction atomicity — per-mutation L2 rollback proof).
func TestL2Atomicity_configpublish_RollsBack_Publish(t *testing.T) {
	bundle := setupPublishBundle(t)
	ctx := context.Background()
	svcCtx := adminIntegCtx()

	// Seed a config entry; the seed itself does not emit a version-published row.
	seedConfigEntry(t, bundle, "rollback.publish.key", "publish-value")

	// Baseline outbox count before the failing Publish.
	before := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigVersionPublished)
	require.Equal(t, 0, before, "baseline: no version-published rows before failing Publish")

	// Inject a writer that always fails, sharing the bundle's repo/txMgr.
	failingWriter := &failOnWriteNumberWriter{
		delegate: adapterpg.NewOutboxWriter(clock.Real()),
		failOn:   1, // fail on the very first Write (the version-published event)
		err:      errors.New("outbox broker down"),
	}
	svcFail, err := NewService(
		clock.Real(), bundle.repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failingWriter))),
		WithTxManager(persistence.WrapForCell(bundle.txMgr)),
	)
	require.NoError(t, err)

	_, err = svcFail.Publish(svcCtx, "rollback.publish.key")
	require.Error(t, err)
	assert.ErrorIs(t, err, failingWriter.err,
		"Publish error must wrap the injected outbox sentinel")

	// Outbox-side: the version-published row must NOT exist (rolled back).
	after := countOutboxRowsByEventType(t, bundle.pool, domain.TopicConfigVersionPublished)
	assert.Equal(t, before, after,
		"version-published outbox row must roll back when outbox write fails")

	// Domain-side: no config_versions row must have been committed.
	// GetVersion requires a configID; obtain it via the live entry.
	liveEntry, getErr := bundle.repo.GetByKey(ctx, "rollback.publish.key")
	require.NoError(t, getErr, "live config_entries row must still exist after rolled-back Publish")
	_, verErr := bundle.repo.GetVersion(ctx, liveEntry.ID, 1)
	require.Error(t, verErr,
		"config_versions row must not exist when outbox write fails (atomic Publish rollback)")

	// Negative control: pass-through Service on a fresh bundle must succeed
	// and leave a config_versions row. This proves Publish genuinely persists
	// the version on the happy path, making the rollback assertion non-vacuous.
	passBundle := setupPublishBundle(t)
	seedConfigEntry(t, passBundle, "rollback.publish.control.key", "control-value")
	_, err = passBundle.svc.Publish(svcCtx, "rollback.publish.control.key")
	require.NoError(t, err, "negative control: Publish must succeed with pass-through writer")
	controlEntry, err := passBundle.repo.GetByKey(ctx, "rollback.publish.control.key")
	require.NoError(t, err)
	_, err = passBundle.repo.GetVersion(ctx, controlEntry.ID, 1)
	require.NoError(t, err,
		"negative control: config_versions row must exist after successful Publish")
}

type failOnWriteNumberWriter struct {
	delegate outbox.Writer
	failOn   int
	calls    int
	err      error
}

func (w *failOnWriteNumberWriter) Write(ctx context.Context, entry outbox.Entry) error {
	w.calls++
	if w.calls == w.failOn {
		return w.err
	}
	return w.delegate.Write(ctx, entry)
}

// TestConcurrentRollback_PG_ExactlyOneWins ports the mem-store unit pattern
// TestConcurrentRollback_ExactlyOneSucceeds (service_test.go) to a real
// PostgreSQL backend. It proves the WHERE key=$X AND version=$expectedVersion
// CAS predicate in cells/configcore/internal/adapters/postgres/config_repo.go
// `doUpdate` serializes concurrent rollbacks at the SQL layer: exactly one
// goroutine sees rowsAffected=1 and commits, the other gets rowsAffected=0,
// triggers `resolveUpdateConflict`, and returns ErrVersionConflict (409).
//
// AI-robust classification: **Medium runtime integration regression guard**
// (testcontainers + concurrent goroutines + CI assertion — runtime invariant
// check, not "violation is unexpressible"). Removing the `AND version=$N`
// predicate is a normal Go code change that compiles cleanly; the CI red is
// what catches the regression.
//
// Mirrors the audit-ledger PG concurrency proof
// `TestAuditLedgerStore_AdvisoryLockSerializesAppend`.
func TestConcurrentRollback_PG_ExactlyOneWins(t *testing.T) {
	t.Parallel()
	bundle := setupPublishBundle(t)
	svcCtx := adminIntegCtx()

	const key = "pg-cas-rollback-key"
	seedConfigEntry(t, bundle, key, "v1")
	// Publish v1 to create the config_versions snapshot Rollback targets.
	// The live entry remains at version 1 after Publish — see the comment in
	// TestRollback_AtomicWithOutbox above.
	_, err := bundle.svc.Publish(svcCtx, key)
	require.NoError(t, err)

	const n = 2
	var (
		successes        atomic.Int32
		versionConflicts atomic.Int32
	)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, rbErr := bundle.svc.Rollback(svcCtx, key, 1, 1)
			switch {
			case rbErr == nil:
				successes.Add(1)
			case isVersionConflictErr(rbErr):
				versionConflicts.Add(1)
			default:
				t.Errorf("unexpected error in concurrent Rollback: %v", rbErr)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(),
		"exactly one concurrent Rollback must succeed at the SQL CAS layer")
	assert.Equal(t, int32(1), versionConflicts.Load(),
		"exactly one concurrent Rollback must yield ErrVersionConflict (409)")

	// Final-state check: the live entry version must have been bumped exactly
	// once (1 → 2) — the losing goroutine must not have written anything.
	finalEntry, err := bundle.repo.GetByKey(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, 2, finalEntry.Version,
		"config_entries.version must increment by exactly 1 across concurrent rollbacks")
}

// TestConcurrentRollback_PG_Sensitive_ExactlyOneWins mirrors
// TestConcurrentRollback_PG_ExactlyOneWins for the sensitive=true SQL branch
// of doUpdate (config_repo.go:553–558 — `UPDATE config_entries SET value=”,
// sensitive=true, ..., value_cipher=$1, ... WHERE key=$5 AND version=$6`).
// This branch lives independently of the sensitive=false branch (line 560–565);
// without an explicit test it could regress (e.g., its CAS predicate removed)
// while sensitive=false coverage keeps passing.
//
// Wires a real LocalAES ValueTransformer (setupPublishBundleEncrypted) so the
// encrypt → BYTEA persist → decrypt round-trip works end-to-end on
// sensitive=true rows; the SQL CAS layer is what's under test, not the cipher
// math, but the encrypted path is required to reach UpdateForRollback at all.
func TestConcurrentRollback_PG_Sensitive_ExactlyOneWins(t *testing.T) {
	t.Parallel()
	bundle := setupPublishBundleEncrypted(t)
	svcCtx := adminIntegCtx()

	const key = "pg-cas-rollback-sensitive-key"
	seedConfigEntryWithSensitivity(t, bundle, key, "v1-secret", true)
	_, err := bundle.svc.Publish(svcCtx, key)
	require.NoError(t, err)

	const n = 2
	var (
		successes        atomic.Int32
		versionConflicts atomic.Int32
	)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, rbErr := bundle.svc.Rollback(svcCtx, key, 1, 1)
			switch {
			case rbErr == nil:
				successes.Add(1)
			case isVersionConflictErr(rbErr):
				versionConflicts.Add(1)
			default:
				t.Errorf("unexpected error in sensitive concurrent Rollback: %v", rbErr)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(),
		"exactly one concurrent Rollback must succeed at the SQL CAS layer (sensitive branch)")
	assert.Equal(t, int32(1), versionConflicts.Load(),
		"exactly one concurrent Rollback must yield ErrVersionConflict (sensitive branch)")

	finalEntry, err := bundle.repo.GetByKey(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, 2, finalEntry.Version,
		"config_entries.version must increment by exactly 1 across concurrent sensitive rollbacks")
	assert.True(t, finalEntry.Sensitive,
		"final entry must remain sensitive=true after sensitive-branch rollback")
}
