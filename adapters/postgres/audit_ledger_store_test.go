//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
)

// newTestLedgerProtocol constructs a Protocol for the "auditcore" namespace used
// throughout these integration tests. Fails the test immediately if construction fails.
func newTestLedgerProtocol(t *testing.T, ns ledger.NamespaceID) *ledger.Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "ledger.NewProtocol for ns=%s", ns)
	return p
}

// newIsolatedLedgerStore creates a fresh per-test DB (migratedPool) and
// returns a *LedgerStore plus its cleanup function. The factory is reusable
// across all sub-tests in this file.
func newIsolatedLedgerStore(
	t *testing.T,
	protocol *ledger.Protocol,
	fc *clockmock.FakeClock,
) (*LedgerStore, func()) {
	t.Helper()

	p := migratedPool(t)
	txm := NewTxManager(p)

	store, err := NewLedgerStore(p.DB(), txm, protocol, fc)
	require.NoError(t, err)

	return store, func() { _ = p.Close(context.Background()) }
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_StoretestSuite: conformance suite (contract parity)
// ---------------------------------------------------------------------------

// TestAuditLedgerStore_StoretestSuite runs the shared storetest.Run suite
// against a real PostgreSQL backend. All cases defined in storetest.Run must
// pass on the PG store to confirm protocol-level contract parity with MemStore.
func TestAuditLedgerStore_StoretestSuite(t *testing.T) {
	protocol := storetest.NewTestProtocol(t)

	factory := storetest.Factory(func(t *testing.T) (ledger.Store, *clockmock.FakeClock, func()) {
		t.Helper()
		fc := clockmock.New(storetest.EpochAnchor())

		p := migratedPool(t)
		txm := NewTxManager(p)
		store, err := NewLedgerStore(p.DB(), txm, protocol, fc)
		require.NoError(t, err)

		cleanupFn := func() { _ = p.Close(context.Background()) }
		return store, fc, cleanupFn
	})

	storetest.Run(t, factory, protocol)
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_RestartRecovery_AcrossPool (B2-C-14)
// ---------------------------------------------------------------------------

// TestAuditLedgerStore_RestartRecovery_AcrossPool verifies that a store
// constructed against pool B correctly reads the tail state written by pool A.
// This simulates an application restart where the new process opens a fresh
// connection pool to the same DB.
//
// Pool A and pool B are TWO independent *pgxpool.Pool instances opened against
// the SAME database DSN (sharedPG.CloneDSN → openPerTestPool twice). This is
// true cross-pool / process-restart semantics — pool B shares no connection,
// prepared-statement cache, or session state with pool A — not merely a fresh
// TxManager over one shared pool.
func TestAuditLedgerStore_RestartRecovery_AcrossPool(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)

	// Pool A: write 5 entries.
	dsn := sharedPG.CloneDSN(t)
	pA := openPerTestPool(t, dsn)

	fcA := clockmock.New(storetest.EpochAnchor())
	txmA := NewTxManager(pA)
	storeA, err := NewLedgerStore(pA.DB(), txmA, protocol, fcA)
	require.NoError(t, err)

	const nFirst = 5
	for i := 1; i <= nFirst; i++ {
		e := storetest.NewEntryFixture(t,
			fmt.Sprintf("restart-evt-a-%d", i),
			"restart.test", "actor", fcA.Now())
		require.NoError(t, storeA.Append(ctx, e), "storeA Append %d", i)
	}
	tailA, err := storeA.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(nFirst), tailA.SeqNo)
	assert.Equal(t, int64(nFirst), tailA.EntryCount)

	// Simulate restart: open an INDEPENDENT second pool to the same DB DSN.
	pB := openPerTestPool(t, dsn)
	fcB := clockmock.New(storetest.EpochAnchor())
	txmB := NewTxManager(pB)
	storeB, err := NewLedgerStore(pB.DB(), txmB, protocol, fcB)
	require.NoError(t, err)

	// storeB must see the tail from storeA's writes.
	tailB_initial, err := storeB.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, tailA.SeqNo, tailB_initial.SeqNo, "storeB must see tailA.SeqNo on restart")
	assert.Equal(t, tailA.PrevHash, tailB_initial.PrevHash, "storeB must see tailA.PrevHash on restart")
	assert.Equal(t, tailA.EntryCount, tailB_initial.EntryCount, "storeB must see tailA.EntryCount on restart")

	// storeB continues writing 5 more entries.
	const nSecond = 5
	for i := 1; i <= nSecond; i++ {
		e := storetest.NewEntryFixture(t,
			fmt.Sprintf("restart-evt-b-%d", i),
			"restart.test", "actor", fcB.Now())
		require.NoError(t, storeB.Append(ctx, e), "storeB Append %d", i)
	}

	// Verify full range 1..10 is valid.
	valid, firstInvalid, err := storeB.Verify(ctx, 1, int64(nFirst+nSecond))
	require.NoError(t, err)
	assert.True(t, valid, "full chain must be valid after cross-pool restart; firstInvalid=%d", firstInvalid)
}

// ---------------------------------------------------------------------------
// TestPGVerify_SubRange_Valid + TestPGVerify_SubRange_Tampered (F-CR-1)
// ---------------------------------------------------------------------------

// TestPGVerify_SubRange_Valid verifies that Verify(fromSeq=2, toSeq=5) returns
// valid=true when the sub-range is intact. This tests the F-CR-1 baseline-fetch
// fix: without the fix, Verify would compare e[2].PrevHash against "" (the zero
// value) and incorrectly report corruption even for a valid chain.
func TestPGVerify_SubRange_Valid(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)

	store, storeCleanup := newIsolatedLedgerStore(t, protocol, clockmock.New(storetest.EpochAnchor()))
	t.Cleanup(storeCleanup)

	fc := clockmock.New(storetest.EpochAnchor())
	const total = 5
	for i := 1; i <= total; i++ {
		e := storetest.NewEntryFixture(t,
			fmt.Sprintf("sub-range-valid-%d", i),
			"sub.range.test", "actor", fc.Now())
		require.NoError(t, store.Append(ctx, e), "Append seq %d", i)
	}

	// Sub-range [2, 5] must be valid (F-CR-1 regression guard).
	valid, firstInvalid, err := store.Verify(ctx, 2, 5)
	require.NoError(t, err)
	assert.True(t, valid, "Verify(2,5) on intact chain must return valid=true; firstInvalid=%d", firstInvalid)
	assert.Equal(t, int64(-1), firstInvalid, "firstInvalid must be -1 for a valid chain")
}

// TestPGVerify_SubRange_Tampered verifies that Verify(fromSeq=2, toSeq=5)
// returns valid=false at seq 3 when entry seq=3's hash is tampered via direct
// SQL UPDATE (PG store; MemStore internal helpers are not available).
func TestPGVerify_SubRange_Tampered(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)

	p := migratedPool(t)
	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(p)
	store, err := NewLedgerStore(p.DB(), txm, protocol, fc)
	require.NoError(t, err)

	const total = 5
	for i := 1; i <= total; i++ {
		e := storetest.NewEntryFixture(t,
			fmt.Sprintf("sub-range-tamper-%d", i),
			"sub.range.tamper", "actor", fc.Now())
		require.NoError(t, store.Append(ctx, e), "Append seq %d", i)
	}

	// Tamper seq=3's hash directly in the DB. The replacement is a valid-format
	// (64-char lowercase hex) but wrong-value hash, so it passes the
	// ck_audit_hash_format CHECK yet still breaks the chain — a realistic tamper
	// that Verify must catch (a malformed value would be rejected by the DB
	// constraint before Verify ever sees it).
	_, execErr := p.DB().Exec(ctx,
		`UPDATE audit_entries SET hash = $2
		 WHERE namespace = $1 AND seq_no = 3`, "auditcore", strings.Repeat("0", 64))
	require.NoError(t, execErr, "direct hash tamper must succeed")

	// Verify sub-range [2, 5] must detect corruption at seq=3 (hash recompute fails).
	valid, firstInvalid, err := store.Verify(ctx, 2, 5)
	require.NoError(t, err)
	assert.False(t, valid, "Verify(2,5) after hash tamper at seq=3 must return valid=false")
	assert.Equal(t, int64(3), firstInvalid,
		"firstInvalid must be 3 (the tampered entry); got %d", firstInvalid)
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_HashFormatConstraint (ck_audit_hash_format)
// ---------------------------------------------------------------------------

// TestAuditLedgerStore_HashFormatConstraint asserts the DB-level CHECK
// constraint ck_audit_hash_format on audit_entries: prev_hash/hash must be
// 64-char lowercase hex (HMAC-SHA256 hex.EncodeToString output), with the
// genesis exception that seq_no=1 carries an empty prev_hash. The constraint is
// seq_no-coupled so an empty prev_hash on a non-genesis row (seq_no>1) is
// rejected, and a non-empty prev_hash on a genesis row (seq_no=1) is rejected.
// This is the secondary DB guard for the tamper-evident chain, alongside
// uq_audit_namespace_seq (020) and the namespace+event_id UNIQUE index (021).
func TestAuditLedgerStore_HashFormatConstraint(t *testing.T) {
	ctx := context.Background()
	p := migratedPool(t)

	// insertRow inserts directly (bypassing the store) so we exercise the DB
	// constraint in isolation. Distinct namespaces avoid uq_audit_namespace_seq
	// collisions across cases.
	insertRow := func(ns string, seq int64, prevHash, hash string) error {
		// event_id is gen_random_uuid()::text so the 021 (namespace, event_id)
		// UNIQUE index never collides — this test isolates ck_audit_hash_format.
		_, err := p.DB().Exec(ctx,
			`INSERT INTO audit_entries
			   (id, namespace, seq_no, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
			 VALUES (gen_random_uuid(), $1, $2, gen_random_uuid()::text, 'type', 'actor', now(), '\x7b7d'::bytea, $3, $4)`,
			ns, seq, prevHash, hash)
		return err
	}

	hexA := strings.Repeat("a", 64) // valid 64-char lowercase hex
	hexB := strings.Repeat("b", 64)

	// Valid: genesis (seq_no=1, empty prev_hash, 64-hex hash).
	require.NoError(t, insertRow("ck-ok-genesis", 1, "", hexA),
		"genesis row (seq_no=1, prev_hash='', 64-hex hash) must satisfy the constraint")
	// Valid: non-genesis chain link (seq_no>1, 64-hex prev_hash + hash).
	require.NoError(t, insertRow("ck-ok-chain", 1, "", hexA))
	require.NoError(t, insertRow("ck-ok-chain", 2, hexA, hexB),
		"non-genesis row with 64-hex prev_hash and hash must satisfy the constraint")

	// Invalid: hash not 64-char hex.
	require.Error(t, insertRow("ck-bad-hashlen", 1, "", "deadbeef"),
		"short hash must violate ck_audit_hash_format")
	require.Error(t, insertRow("ck-bad-hashfmt", 1, "", strings.Repeat("Z", 64)),
		"non-hex (uppercase Z) hash must violate ck_audit_hash_format")
	// Invalid: non-genesis row (seq_no>1) with empty prev_hash.
	require.NoError(t, insertRow("ck-bad-prev", 1, "", hexA))
	require.Error(t, insertRow("ck-bad-prev", 2, "", hexB),
		"empty prev_hash on a non-genesis row (seq_no>1) must violate ck_audit_hash_format")
	// Invalid: non-genesis row with a non-hex (uppercase) prev_hash — the regex
	// guards prev_hash as well as hash.
	require.NoError(t, insertRow("ck-bad-prevfmt", 1, "", hexA))
	require.Error(t, insertRow("ck-bad-prevfmt", 2, strings.Repeat("A", 64), hexB),
		"uppercase (non-hex) prev_hash on a non-genesis row must violate ck_audit_hash_format")
	// Invalid: genesis row (seq_no=1) with a non-empty prev_hash.
	require.Error(t, insertRow("ck-bad-genesis", 1, hexA, hexB),
		"non-empty prev_hash on a genesis row (seq_no=1) must violate ck_audit_hash_format")
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_AdvisoryLockSerializesAppend (B2-C-10)
// ---------------------------------------------------------------------------

// TestAuditLedgerStore_AdvisoryLockSerializesAppend verifies that 100
// concurrent Append calls produce a valid, gap-free sequence (1..100) and
// that the final hash chain is intact. This proves pg_advisory_xact_lock
// serializes Append within the namespace.
func TestAuditLedgerStore_AdvisoryLockSerializesAppend(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)

	p := migratedPool(t)
	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(p)
	store, err := NewLedgerStore(p.DB(), txm, protocol, fc)
	require.NoError(t, err)

	const n = 100
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			e := storetest.NewEntryFixture(t,
				fmt.Sprintf("advisory-lock-evt-%03d", i),
				"lock.test", "actor", fc.Now())
			if err := store.Append(ctx, e); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for e := range errCh {
		t.Errorf("concurrent Append error: %v", e)
	}

	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(n), tail.SeqNo, "SeqNo must be %d after %d appends", n, n)
	assert.Equal(t, int64(n), tail.EntryCount, "EntryCount must be %d", n)

	// Verify the full hash chain is intact.
	valid, firstInvalid, err := store.Verify(ctx, 1, int64(n))
	require.NoError(t, err)
	assert.True(t, valid, "hash chain must be valid after %d concurrent appends; firstInvalid=%d", n, firstInvalid)
}

// ---------------------------------------------------------------------------
// TestL2Atomicity_auditcore_RollsBack (AUDITAPPEND-L2-FAILURE-PROOF-01 / L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_auditcore_RollsBack proves that when an outbox write fails
// inside the same transaction as store.Append, the entire transaction rolls
// back and no audit_entries row is written.
//
// This test satisfies L2-OUTBOX-ATOMICITY-COVERAGE-01 for the auditcore
// cell-level L2 unit (cell-level test; shared appender package covers the
// hybrid consumer path via TestL2Atomicity_appender_{RollsBack,ReplayIdempotent}).
//
// Design: the test injects a "fail-injecting outbox writer" that runs inside
// the same txRunner.RunInTx block. store.Append succeeds within the tx, then
// the outbox writer deliberately returns an error, causing RunInTx to rollback
// the whole transaction. We then assert audit_entries has no new rows.
func TestL2Atomicity_auditcore_RollsBack(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)

	p := migratedPool(t)
	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(p)
	store, err := NewLedgerStore(p.DB(), txm, protocol, fc)
	require.NoError(t, err)

	// Count rows before the aborted transaction.
	var countBefore int
	require.NoError(t, p.DB().QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE namespace = $1", string(ns)).
		Scan(&countBefore))

	// Run store.Append + deliberate outbox-fail inside the same transaction.
	simulatedOutboxErr := errors.New("simulated outbox write failure")
	txErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		e := storetest.NewEntryFixture(t, "atomicity-evt-1", "atomicity.test", "actor", fc.Now())
		if appendErr := store.Append(txCtx, e); appendErr != nil {
			return appendErr
		}
		// Simulate outbox write failure — this forces RunInTx to rollback.
		return simulatedOutboxErr
	})
	require.Error(t, txErr, "RunInTx must return error after outbox failure")
	require.ErrorIs(t, txErr, simulatedOutboxErr)

	// After rollback, audit_entries must have no new rows.
	var countAfter int
	require.NoError(t, p.DB().QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE namespace = $1", string(ns)).
		Scan(&countAfter))
	assert.Equal(t, countBefore, countAfter,
		"store.Append must roll back atomically with outbox failure: no new row must persist")

	// Negative control: a successful Append on the same store must persist a
	// row, proving the rollback assertion above is not vacuous (i.e., Append
	// genuinely writes a row on the happy path and the store is properly wired).
	e2 := storetest.NewEntryFixture(t, "atomicity-control-evt", "atomicity.control", "actor", fc.Now())
	require.NoError(t, store.Append(ctx, e2), "negative control: Append must succeed without outbox failure")
	var countControl int
	require.NoError(t, p.DB().QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE namespace = $1", string(ns)).
		Scan(&countControl))
	assert.Equal(t, countAfter+1, countControl,
		"negative control: successful Append must persist exactly one new audit_entries row")
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_NamespaceIsolation
// ---------------------------------------------------------------------------

// TestAuditLedgerStore_NamespaceIsolation verifies that two stores sharing the
// same physical table but different namespace IDs do not pollute each other's
// entries, and that their advisory locks are independent (different hash inputs).
func TestAuditLedgerStore_NamespaceIsolation(t *testing.T) {
	ctx := context.Background()

	p := migratedPool(t)

	nsA, err := ledger.ParseNamespaceID("audit_a")
	require.NoError(t, err)
	nsB, err := ledger.ParseNamespaceID("audit_b")
	require.NoError(t, err)

	protoA := newTestLedgerProtocol(t, nsA)
	protoB := newTestLedgerProtocol(t, nsB)

	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(p)

	storeA, err := NewLedgerStore(p.DB(), txm, protoA, fc)
	require.NoError(t, err)
	storeB, err := NewLedgerStore(p.DB(), txm, protoB, fc)
	require.NoError(t, err)

	// Write 3 entries to storeA and 2 to storeB.
	for i := 1; i <= 3; i++ {
		e := storetest.NewEntryFixture(t,
			fmt.Sprintf("ns-iso-a-evt-%d", i),
			"iso.test", "actor-a", fc.Now())
		require.NoError(t, storeA.Append(ctx, e), "storeA Append %d", i)
	}
	for i := 1; i <= 2; i++ {
		e := storetest.NewEntryFixture(t,
			fmt.Sprintf("ns-iso-b-evt-%d", i),
			"iso.test", "actor-b", fc.Now())
		require.NoError(t, storeB.Append(ctx, e), "storeB Append %d", i)
	}

	// Each store's SeqNo starts from 1 independently.
	tailA, err := storeA.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), tailA.SeqNo, "namespace A SeqNo must be 3")

	tailB, err := storeB.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), tailB.SeqNo, "namespace B SeqNo must be 2")

	// Query storeA must not return storeB's entries.
	aEntries, err := storeA.Query(ctx, ledger.AuditFilters{}, query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	require.NoError(t, err)
	assert.Len(t, aEntries, 3, "namespace A Query must return exactly 3 entries")
	for _, e := range aEntries {
		assert.Equal(t, "actor-a", e.ActorID, "namespace A entry must have actor-a")
	}

	bEntries, err := storeB.Query(ctx, ledger.AuditFilters{}, query.ListParams{Limit: 50, Sort: ledger.QuerySort()})
	require.NoError(t, err)
	assert.Len(t, bEntries, 2, "namespace B Query must return exactly 2 entries")
	for _, e := range bEntries {
		assert.Equal(t, "actor-b", e.ActorID, "namespace B entry must have actor-b")
	}

	// Verify each namespace's chain independently.
	validA, _, err := storeA.Verify(ctx, 1, 3)
	require.NoError(t, err)
	assert.True(t, validA, "namespace A chain must be valid")

	validB, _, err := storeB.Verify(ctx, 1, 2)
	require.NoError(t, err)
	assert.True(t, validB, "namespace B chain must be valid")
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_RepoReadiness_Conformance (healthz.RepoProber)
// ---------------------------------------------------------------------------

// TestAuditLedgerStore_RepoReadiness_Conformance runs the single-source
// RepoProber conformance harness against LedgerStore. It verifies that:
//   - healthy: RepoReady returns nil when the audit_entries table is present.
//   - broken: RepoReady returns a non-nil error when audit_entries is dropped,
//     exercising a failure domain that a pool-level ping cannot detect.
func TestAuditLedgerStore_RepoReadiness_Conformance(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)
	fc := clockmock.New(storetest.EpochAnchor())

	// healthy: per-test DB with migrations applied.
	healthyStore, healthyCleanup := newIsolatedLedgerStore(t, protocol, fc)
	t.Cleanup(healthyCleanup)

	// broken: per-test DB with audit_entries table dropped after migration.
	brokenPool := migratedPool(t)
	_, execErr := brokenPool.DB().Exec(ctx, `DROP TABLE IF EXISTS audit_entries CASCADE`)
	require.NoError(t, execErr, "drop audit_entries for broken scenario")

	brokenTxm := NewTxManager(brokenPool)
	brokenStore, err := NewLedgerStore(brokenPool.DB(), brokenTxm, protocol, clockmock.New(storetest.EpochAnchor()))
	require.NoError(t, err)

	celltest.RunRepoReadinessConformance(t, "ledger-pg", healthyStore, brokenStore)
}

// ---------------------------------------------------------------------------
// TestAuditLedgerStore_ReadWithinAmbientTx (PR578-FU)
// ---------------------------------------------------------------------------

// errRollbackSentinel forces the outer RunInTx to roll back so the test can
// assert that reads performed inside the ambient transaction observed the
// uncommitted chain state while the post-rollback state is empty.
var errRollbackSentinel = errors.New("intentional rollback")

// TestAuditLedgerStore_ReadWithinAmbientTx makes the LedgerStore godoc caveat
// ("when called within a caller's ambient transaction, the result reflects
// that transaction's uncommitted chain state") falsifiable. Tail/GetBySeq/
// Verify all route through pgExecutor, so an Append performed inside an outer
// RunInTx must be visible to those reads in the SAME txCtx before commit, and
// invisible after a rollback. A committed control proves the in-tx read is not
// an artifact and the persisted path still works.
func TestAuditLedgerStore_ReadWithinAmbientTx(t *testing.T) {
	ctx := context.Background()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	protocol := newTestLedgerProtocol(t, ns)

	p := migratedPool(t)
	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(p)
	store, err := NewLedgerStore(p.DB(), txm, protocol, fc)
	require.NoError(t, err)

	// --- Rolled-back transaction: in-tx reads see the uncommitted entry,
	//     post-rollback reads see an empty ledger. ---
	rbErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		e := storetest.NewEntryFixture(t, "ambient-read-1", "ambient.read", "actor", fc.Now())
		require.NoError(t, store.Append(txCtx, e), "Append inside ambient tx")

		tail, tErr := store.Tail(txCtx)
		require.NoError(t, tErr)
		assert.Equal(t, int64(1), tail.SeqNo, "Tail(txCtx) must see the uncommitted append")
		assert.Equal(t, int64(1), tail.EntryCount, "EntryCount(txCtx) must reflect uncommitted state")

		got, gErr := store.GetBySeq(txCtx, 1)
		require.NoError(t, gErr, "GetBySeq(txCtx,1) must see the uncommitted append")
		assert.Equal(t, int64(1), got.SeqNo)

		valid, firstInvalid, vErr := store.Verify(txCtx, 1, 1)
		require.NoError(t, vErr)
		assert.True(t, valid, "Verify(txCtx,1,1) over uncommitted chain must be valid")
		// Verify contract: firstInvalidSeq is -1 when the entire range is intact
		// (LedgerStore mirrors mem_store.go:234 — `return true, -1, nil`).
		assert.Equal(t, int64(-1), firstInvalid)

		return errRollbackSentinel
	})
	require.ErrorIs(t, rbErr, errRollbackSentinel, "RunInTx must surface the rollback sentinel")

	// Fresh context (no ambient tx) → pgExecutor falls back to the pool, which
	// only sees committed state. The rolled-back append must be gone.
	tail, err := store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), tail.SeqNo, "post-rollback Tail must be empty")
	assert.Equal(t, int64(0), tail.EntryCount, "post-rollback EntryCount must be 0")

	_, err = store.GetBySeq(ctx, 1)
	require.Error(t, err, "post-rollback GetBySeq(1) must not find the rolled-back entry")

	// --- Committed control: a successful RunInTx persists; reads outside the
	//     tx then observe it. ---
	commitErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		e := storetest.NewEntryFixture(t, "ambient-read-2", "ambient.read", "actor", fc.Now())
		require.NoError(t, store.Append(txCtx, e))
		return nil
	})
	require.NoError(t, commitErr)

	tail, err = store.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), tail.SeqNo, "committed entry must be seq 1 (rolled-back one consumed no seq)")
	assert.Equal(t, int64(1), tail.EntryCount, "committed Tail must count the persisted entry")
}
