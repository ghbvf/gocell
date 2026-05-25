//go:build integration

package saga_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/postgres/saga"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	sagamod "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// TestPGSagaJournal_ConformanceSuite verifies that PGJournal satisfies the
// full journal.Journal conformance contract defined in sagajournaltest. This
// is the PR-04 SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 entry point — once this
// file exists, the archtest in tools/archtest considers PGJournal enrolled.
//
// Each subtest gets a fresh per-test database via pgshare.NewPerTestPool, so
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

	claimed, leaseA, err := j.ClaimPending(ctx, 10, 10*time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, claimed)
	_ = leaseA

	// Advance past lease expiry, reclaim with worker B.
	clk.Advance(15 * time.Second)
	_, leaseB, err := j.ClaimPending(ctx, 10, 10*time.Second)
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
		mu      sync.Mutex
		claims  = make(map[idutil.SafeID]int) // id → claim count
		errs    []error
	)
	var wg sync.WaitGroup
	for range G {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, e := j.ClaimPending(ctx, 5, 10*time.Second)
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
