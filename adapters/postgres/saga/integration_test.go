//go:build integration

package saga_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/postgres/saga"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	sagamod "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// Test-time lease durations and the post-expiry advance step, extracted
// per TEST-TIME-LITERAL-01 (forbids inline time.Duration literals in test
// files). All PG integration tests pin to the same window so behavior is
// consistent with conformance suite's shortLease (10s) baseline.
const (
	testLeaseWindow      = 10 * time.Second
	testPastLeaseAdvance = 15 * time.Second // > testLeaseWindow → forces stale lease
)

// TestPGSagaJournal_ConformanceSuite verifies that PGJournal satisfies the
// full journal.Journal conformance contract defined in sagajournaltest. This
// is the PR-04 SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 entry point — once this
// file exists, the archtest in tools/archtest considers PGJournal enrolled.
//
// Each subtest gets a fresh per-test database via pgtest.NewPerTestPool, so
// TRUNCATE is unnecessary; the conformance suite Factory is invoked once per
// case with an isolated DB.
func TestPGSagaJournal_ConformanceSuite(t *testing.T) {
	factory := func(t *testing.T) (journal.Journal, *clockmock.FakeClock, func()) {
		t.Helper()
		pool := sharedPG.NewPerTestPool(t)
		// FakeClock starts at a deterministic UTC instant so PG timestamptz
		// columns compare consistently across runs.
		clk := clockmock.New(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
		j, err := saga.NewJournal(pool.DB(), clk)
		require.NoError(t, err)
		return j, clk, func() {}
	}

	sagajournaltest.RunConformanceSuite(t, factory)
}

// TestPGSagaJournal_StaleAppendIsCAS_NotConstraintViolation verifies the PG
// fencing model surfaces stale-lease Append as a clean (no rows updated)
// outcome at the SQL layer — not as a row-level CHECK or constraint
// violation. Important because the SQL CAS pattern relies on RowsAffected==0
// to drive the typed ErrSagaStaleLease error; if PG instead raised a
// constraint error, the error classification path would change.
func TestPGSagaJournal_StaleAppendIsCAS_NotConstraintViolation(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	inst := sagajournaltest.NewInstanceFixture(t, "pg-stale-cas", clk.Now())
	require.NoError(t, j.Enqueue(ctx, inst))

	claimed, leaseA, err := j.ClaimPending(ctx, 10, testLeaseWindow)
	require.NoError(t, err)
	require.NotEmpty(t, claimed)
	_ = leaseA

	// Advance past lease expiry, reclaim with worker B.
	clk.Advance(testPastLeaseAdvance)
	_, leaseB, err := j.ClaimPending(ctx, 10, testLeaseWindow)
	require.NoError(t, err)
	require.NotEqual(t, leaseA, leaseB, "worker B lease must differ from A's")

	// Worker A's stale Append must return ErrSagaStaleLease — verifies the
	// CAS path surfaces as the typed Code, not a PG-level constraint error.
	_, err = j.Append(ctx, inst.ID, leaseA, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: idutil.SafeID("step-one"),
	})
	require.Error(t, err, "stale Append must error")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "stale Append error must unwrap to *errcode.Error")
	require.Equal(t, errcode.ErrSagaStaleLease, ec.Code,
		"stale Append must return ErrSagaStaleLease (got %q)", ec.Code)
}

// TestPGSagaJournal_ClaimPending_Concurrent_NoDuplicate exercises the
// FOR UPDATE SKIP LOCKED contract under real PG concurrency. Goroutine
// safety is part of the conformance suite, but the PG-specific concern is
// whether PostgreSQL serializes the CTE materialisation correctly; rerun
// here with a larger fanout against a real backend.
func TestPGSagaJournal_ClaimPending_Concurrent_NoDuplicate(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	const total = 30
	for i := range total {
		id := idutil.SafeID("pg-concurrent-claim-" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
		require.NoError(t, j.Enqueue(ctx, sagamod.NewInstance(id, id+"-def", clk.Now())))
	}

	const G = 8
	var (
		mu     sync.Mutex
		claims = make(map[idutil.SafeID]int) // id → claim count
		errs   []error
	)
	var wg sync.WaitGroup
	for range G {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, e := j.ClaimPending(ctx, 5, testLeaseWindow)
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				errs = append(errs, e)
				return
			}
			for _, ci := range out {
				claims[ci.Instance.ID]++
			}
		}()
	}
	wg.Wait()

	require.Empty(t, errs, "concurrent ClaimPending errors")
	// Conservation: every enqueued instance must be claimed exactly once. A
	// silent batch loss would leave len(claims) < total, which the per-id
	// count loop alone would not catch.
	require.Equal(t, total, len(claims),
		"all %d enqueued instances must be claimed exactly once (got %d distinct claimed)", total, len(claims))
	for id, count := range claims {
		require.Equal(t, 1, count, "instance %s claimed %d times (must be 1)", id, count)
	}
}

// ---------------------------------------------------------------------------
// Ambient-tx integration: Append / MarkTerminal must join the caller's
// transaction so an outer rollback discards the journal write atomically.
// This is the PR-04 review C1 contract: PGJournal MUST NOT open its own tx
// when the ctx already carries one (kernel/persistence.TxFromContext), or
// the L2 OutboxFact invariant ("saga.Append + outbox.Emit atomic") breaks.
// ---------------------------------------------------------------------------

// sentinelOuterFailure is the error returned by the outer RunInTx callback
// to force a rollback after a successful inner journal write. Defined as a
// package-level value so test asserts can errors.Is against it.
var sentinelOuterFailure = errors.New("outer ambient tx forced failure")

// TestPGSagaJournal_AppendInsideAmbientTx_RollsBackOnOuterFailure verifies
// that an Append landed inside a txRunner.RunInTx block is rolled back when
// the outer callback returns an error — saga_events MUST be empty after the
// failed outer tx commits its rollback.
func TestPGSagaJournal_AppendInsideAmbientTx_RollsBackOnOuterFailure(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	inst := sagajournaltest.NewInstanceFixture(t, "pg-ambient-rollback-append", clk.Now())
	require.NoError(t, j.Enqueue(ctx, inst))

	claimed, _, err := j.ClaimPending(ctx, 1, testLeaseWindow)
	require.NoError(t, err)
	require.NotEmpty(t, claimed)
	ci := claimed[0]

	txm := adapterpg.NewTxManager(pool)
	rollbackErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		_, appendErr := j.Append(txCtx, ci.Instance.ID, ci.LeaseID, journal.Event{
			Kind:     journal.KindStepStarted,
			StepName: idutil.SafeID("step-one"),
			Payload:  []byte(`{"k":"v"}`),
		})
		require.NoError(t, appendErr, "Append inside ambient tx must succeed")
		return sentinelOuterFailure // force outer rollback AFTER successful Append
	})
	require.ErrorIs(t, rollbackErr, sentinelOuterFailure)

	// saga_events must be empty for this instance — Append's INSERT was
	// rolled back together with the outer tx.
	var eventCount int
	err = pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM saga_events WHERE instance_id = $1`, string(inst.ID)).Scan(&eventCount)
	require.NoError(t, err)
	require.Equal(t, 0, eventCount,
		"saga_events must be empty after outer RunInTx rollback (ambient tx atomicity)")

	// Projection must also be untouched — current_version still 0.
	var version int64
	err = pool.DB().QueryRow(ctx,
		`SELECT current_version FROM saga_instances WHERE id = $1`, string(inst.ID)).Scan(&version)
	require.NoError(t, err)
	require.Equal(t, int64(0), version,
		"saga_instances.current_version must be unchanged after outer rollback")
}

// TestPGSagaJournal_MarkTerminalInsideAmbientTx_RollsBackOnOuterFailure: same
// invariant for MarkTerminal — outer rollback discards both the projection
// flip and the terminal event row.
func TestPGSagaJournal_MarkTerminalInsideAmbientTx_RollsBackOnOuterFailure(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	inst := sagajournaltest.NewInstanceFixture(t, "pg-ambient-rollback-mt", clk.Now())
	require.NoError(t, j.Enqueue(ctx, inst))

	claimed, _, err := j.ClaimPending(ctx, 1, testLeaseWindow)
	require.NoError(t, err)
	require.NotEmpty(t, claimed)
	ci := claimed[0]

	txm := adapterpg.NewTxManager(pool)
	rollbackErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		ok, mtErr := j.MarkTerminal(txCtx, ci.Instance.ID, ci.LeaseID, sagamod.StatusFailed)
		require.NoError(t, mtErr)
		require.True(t, ok, "MarkTerminal inside ambient tx must succeed")
		return sentinelOuterFailure
	})
	require.ErrorIs(t, rollbackErr, sentinelOuterFailure)

	// saga_events must be empty AND saga_instances.status must remain Pending
	// (not Failed) — both writes rolled back with the outer tx.
	var eventCount int
	err = pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM saga_events WHERE instance_id = $1`, string(inst.ID)).Scan(&eventCount)
	require.NoError(t, err)
	require.Equal(t, 0, eventCount, "saga_events must be empty after outer rollback")

	var status int16
	err = pool.DB().QueryRow(ctx,
		`SELECT status FROM saga_instances WHERE id = $1`, string(inst.ID)).Scan(&status)
	require.NoError(t, err)
	require.Equal(t, int16(sagamod.StatusPending), status,
		"status must remain Pending after MarkTerminal outer rollback")
}

// TestPGSagaJournal_AppendOutsideAmbientTx_StillAtomic ensures the no-
// ambient path (caller has no outer RunInTx) still uses an internally-
// opened tx so the multi-statement Append remains atomic.
func TestPGSagaJournal_AppendOutsideAmbientTx_StillAtomic(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	inst := sagajournaltest.NewInstanceFixture(t, "pg-noambient-append", clk.Now())
	require.NoError(t, j.Enqueue(ctx, inst))
	claimed, _, err := j.ClaimPending(ctx, 1, testLeaseWindow)
	require.NoError(t, err)
	require.NotEmpty(t, claimed)
	ci := claimed[0]

	version, err := j.Append(ctx, ci.Instance.ID, ci.LeaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: idutil.SafeID("step-one"),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), version)

	events, err := j.Load(ctx, ci.Instance.ID)
	require.NoError(t, err)
	require.Len(t, events, 1, "no-ambient Append committed; Load returns the single event")

	var curVersion int64
	err = pool.DB().QueryRow(ctx,
		`SELECT current_version FROM saga_instances WHERE id = $1`, string(inst.ID)).Scan(&curVersion)
	require.NoError(t, err)
	require.Equal(t, int64(1), curVersion,
		"current_version must advance when caller has no ambient tx (saga opens its own)")
}

// TestPGSagaJournal_RepoReady_SchemaBroken closes the conformance gap left
// by sagajournaltest.RunConformanceSuite/RepoReady/schema-broken (skipped for
// memjournal — no differentiated failure domain). Verifies that dropping
// EITHER saga relation causes RepoReady to surface a non-nil error, locking
// the UNION ALL double-probe contract (saga_instances + saga_events).
//
// Two subcases, each on its own per-test database so the schema mutation in
// one does not leak into the other.
func TestPGSagaJournal_RepoReady_SchemaBroken(t *testing.T) {
	cases := []string{"saga_events", "saga_instances"}
	for _, table := range cases {
		t.Run("drop_"+table, func(t *testing.T) {
			pool := sharedPG.NewPerTestPool(t)
			clk := clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))
			j, err := saga.NewJournal(pool.DB(), clk)
			require.NoError(t, err)

			ctx := context.Background()
			// Sanity: healthy schema → nil.
			require.NoError(t, j.RepoReady(ctx),
				"RepoReady on healthy schema must return nil before mutation")

			// Drop one of the probed tables. CASCADE handles the FK from
			// saga_events → saga_instances when the parent goes first.
			_, err = pool.DB().Exec(ctx, "DROP TABLE "+table+" CASCADE")
			require.NoError(t, err, "DROP TABLE %s setup must succeed", table)

			err = j.RepoReady(ctx)
			require.Error(t, err,
				"RepoReady after DROP TABLE %s must surface a non-nil error "+
					"(UNION ALL double-probe contract)", table)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec),
				"RepoReady error must unwrap to *errcode.Error")
			require.Equal(t, errcode.KindInternal, ec.Kind,
				"RepoReady schema-broken error must carry KindInternal")
		})
	}
}

// TestPGSagaJournal_ClaimPendingInsideAmbientTx_RollsBackOnOuterFailure
// guards the ClaimPending branch: even though ClaimPending uses a single
// CTE (no explicit Begin in saga), it routes through pgExecutor which
// joins ambient tx — so a leased instance MUST revert to claimable when
// the outer tx rolls back.
func TestPGSagaJournal_ClaimPendingInsideAmbientTx_RollsBackOnOuterFailure(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	inst := sagajournaltest.NewInstanceFixture(t, "pg-ambient-claim-rollback", clk.Now())
	require.NoError(t, j.Enqueue(ctx, inst))

	txm := adapterpg.NewTxManager(pool)
	rollbackErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		claimed, leaseID, claimErr := j.ClaimPending(txCtx, 1, testLeaseWindow)
		require.NoError(t, claimErr)
		require.NotEmpty(t, claimed, "ClaimPending inside ambient tx must see the enqueued instance")
		require.NotEmpty(t, leaseID)
		return sentinelOuterFailure
	})
	require.ErrorIs(t, rollbackErr, sentinelOuterFailure)

	// Instance must still be claimable — the lease minted inside the rolled-
	// back tx must not persist.
	var leaseSet bool
	err = pool.DB().QueryRow(ctx,
		`SELECT lease_id IS NOT NULL FROM saga_instances WHERE id = $1`, string(inst.ID)).Scan(&leaseSet)
	require.NoError(t, err)
	require.False(t, leaseSet,
		"lease_id must be NULL after outer RunInTx rollback (ClaimPending must join ambient tx)")
}
