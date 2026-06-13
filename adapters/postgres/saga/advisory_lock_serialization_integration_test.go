//go:build integration

package saga_test

// TestPGSagaJournal_AdvisoryLock_SerializesGlobalSeq proves that the
// pg_advisory_xact_lock in Append (sagaEventsGlobalAppendLockKey) serializes
// saga_events INSERTs so global_seq order equals commit order (F1, #1630).
//
// Design (deterministic, no sleep):
//
//  1. goroutine-1 opens a RunInTx, Appends to instA, then signals "appended"
//     and waits on "release" before committing (returning nil = commit).
//     While goroutine-1 holds the advisory lock, the IDENTITY value for instA's
//     row has been allocated (seq=N).
//
//  2. Main waits for "appended", then starts goroutine-2 in a RunInTx that
//     Appends to instB. Because goroutine-1 still holds the advisory lock,
//     goroutine-2 BLOCKS at pg_advisory_xact_lock($1) inside its INSERT path.
//
//  3. Main asserts goroutine-2 does NOT complete within blockAssertTimeout
//     (proving the advisory lock is actually held and the second append is
//     serialized). It then signals "release" so goroutine-1 commits.
//
//  4. After goroutine-1 commits (releases the advisory lock), goroutine-2
//     unblocks, allocates seq=N+1 for instB, and commits.
//
//  5. Assert: instA's GlobalSeq < instB's GlobalSeq (commit order = seq order).
//
// This also confirms RunGlobalReaderConformance (including
// GlobalSeq_ConcurrentAppend_NoDupContiguous) still passes after the lock is
// in place — that case is covered by the separate conformance enrollment in
// pg_global_reader_integration_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/postgres/saga"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
)

// Timing constants extracted per TEST-TIME-LITERAL-01.
const (
	// advisoryLockHoldTimeout is the maximum time we wait for goroutine-1 to
	// acquire the row (signal "appended") before failing the test.
	advisoryLockHoldTimeout = 5 * time.Second

	// advisoryLockBlockAssertTimeout is the window during which goroutine-2 must
	// NOT complete (proving the advisory lock blocks the second append).
	advisoryLockBlockAssertTimeout = 200 * time.Millisecond

	// advisoryLockReleaseTimeout is the max time we wait for goroutine-2 to
	// finish after goroutine-1 has committed (released the lock).
	advisoryLockReleaseTimeout = 5 * time.Second
)

// TestPGSagaJournal_AdvisoryLock_SerializesGlobalSeq verifies that the
// transaction-scoped advisory lock (sagaEventsGlobalAppendLockKey) in
// PGJournal.Append serializes saga_events INSERTs so that global_seq order
// equals commit order. This is the direct regression test for F1 in PR #1630.
func TestPGSagaJournal_AdvisoryLock_SerializesGlobalSeq(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC))
	j, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)

	ctx := context.Background()
	txm := adapterpg.NewTxManager(pool)

	// Enqueue two independent saga instances.
	instA := sagajournaltest.NewInstanceFixture(t, "advisory-lock-inst-a", clk.Now())
	instB := sagajournaltest.NewInstanceFixture(t, "advisory-lock-inst-b", clk.Now())
	require.NoError(t, j.Enqueue(ctx, instA))
	require.NoError(t, j.Enqueue(ctx, instB))

	// Claim both instances so we hold valid leases.
	claimedA, leaseA, err := j.ClaimPending(ctx, 1, testLeaseWindow)
	require.NoError(t, err)
	require.Len(t, claimedA, 1)

	// Advance clock so instB has a different started_at (prevents race on
	// ClaimPending ordering; both are in Pending status).
	clk.Advance(time.Second)

	claimedB, leaseB, err := j.ClaimPending(ctx, 1, testLeaseWindow)
	require.NoError(t, err)
	require.Len(t, claimedB, 1)

	// appended signals that goroutine-1 has appended to instA inside its tx
	// and is now holding the advisory lock.
	appended := make(chan struct{})
	// release signals goroutine-1 to commit (and release the advisory lock).
	release := make(chan struct{})

	var errG1 error
	g1Done := make(chan struct{})

	// Goroutine-1: Append to instA inside a RunInTx. After the Append succeeds
	// (advisory lock is held, IDENTITY seq allocated for instA), signal "appended"
	// and wait on "release" before the function returns (commit).
	go func() {
		defer close(g1Done)
		errG1 = txm.RunInTx(ctx, func(txCtx context.Context) error {
			_, appendErr := j.Append(txCtx, instA.ID, leaseA, journal.Event{
				Kind:     journal.KindStepStarted,
				StepName: "step-a",
			})
			if appendErr != nil {
				return appendErr
			}
			// Advisory lock is now held for the duration of this tx.
			close(appended)
			// Wait for the main goroutine to assert goroutine-2 is blocked,
			// then release by returning nil (= commit).
			<-release
			return nil
		})
	}()

	// Wait for goroutine-1 to hold the advisory lock.
	select {
	case <-appended:
	case <-time.After(advisoryLockHoldTimeout):
		t.Fatal("goroutine-1 did not acquire the advisory lock within the expected window")
	}

	// Goroutine-2: Append to instB inside its own RunInTx. This MUST block at
	// pg_advisory_xact_lock inside Append because goroutine-1 holds the lock.
	g2Result := make(chan error, 1)
	go func() {
		g2Result <- txm.RunInTx(ctx, func(txCtx context.Context) error {
			_, appendErr := j.Append(txCtx, instB.ID, leaseB, journal.Event{
				Kind:     journal.KindStepStarted,
				StepName: "step-b",
			})
			return appendErr
		})
	}()

	// Assert goroutine-2 is blocked (advisory lock is held by goroutine-1).
	select {
	case e := <-g2Result:
		t.Fatalf("goroutine-2 returned before goroutine-1 released the advisory lock (expected block), got: %v", e)
	case <-time.After(advisoryLockBlockAssertTimeout):
		// Expected: goroutine-2 is blocked — proves the advisory lock is held.
	}

	// Release goroutine-1: commit, releasing the advisory lock.
	close(release)
	select {
	case <-g1Done:
	case <-time.After(advisoryLockReleaseTimeout):
		t.Fatal("goroutine-1 did not finish after release signal")
	}
	require.NoError(t, errG1, "goroutine-1 (Append instA) must succeed")

	// Goroutine-2 now unblocks: it allocates its IDENTITY seq AFTER goroutine-1
	// committed, so instB.global_seq > instA.global_seq.
	var errG2 error
	select {
	case errG2 = <-g2Result:
	case <-time.After(advisoryLockReleaseTimeout):
		t.Fatal("goroutine-2 did not finish after goroutine-1 released the advisory lock")
	}
	require.NoError(t, errG2, "goroutine-2 (Append instB) must succeed")

	// Verify global_seq order equals commit order:
	// instA committed first → instA.global_seq < instB.global_seq.
	var seqA, seqB int64
	err = pool.DB().QueryRow(ctx,
		`SELECT global_seq FROM saga_events WHERE instance_id = $1`, string(instA.ID)).Scan(&seqA)
	require.NoError(t, err, "read global_seq for instA")

	err = pool.DB().QueryRow(ctx,
		`SELECT global_seq FROM saga_events WHERE instance_id = $1`, string(instB.ID)).Scan(&seqB)
	require.NoError(t, err, "read global_seq for instB")

	assert.Greater(t, seqA, int64(0), "instA.global_seq must be > 0 (IDENTITY starts at 1)")
	assert.Greater(t, seqB, int64(0), "instB.global_seq must be > 0 (IDENTITY starts at 1)")
	assert.Less(t, seqA, seqB,
		"instA committed before instB (goroutine-1 released lock first): instA.global_seq=%d must be < instB.global_seq=%d",
		seqA, seqB)
}
