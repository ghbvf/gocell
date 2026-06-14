//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/projection/projectiontest"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// txHoldDuration is the max time tokenA may wait for tokenB to release the
// SELECT FOR UPDATE row lock in the concurrent leader-handoff test.
const txHoldDuration = testtime.D2s

// blockAssertTimeout is the polling deadline used to assert that tokenA is
// blocked on the row lock before tokenB commits.
const blockAssertTimeout = testtime.D200ms

// TestPGOwnerCheckpointStore_Conformance enrolls the concrete
// *ProjectionCheckpointStore in the shared OwnerCheckpointStore conformance
// harness (SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01). The harness drives
// AdvanceIfOwner / LoadOffset against real PG, exercising the CAS semantics B:
// cold reject, cold claim, same-owner re-advance (forward + backward),
// new-leader claim-ahead, stale-owner reject, empty-token fail-closed.
func TestPGOwnerCheckpointStore_Conformance(t *testing.T) {
	store, _ := newCheckpointStore(t)
	projectiontest.RunOwnerCheckpointConformance(t, store)
}

// TestPGOwnerCheckpointStore_ColdConcurrent_SameKey verifies that two goroutines
// racing AdvanceIfOwner on the same (cell, projection) key on a fresh store
// (cold-start race) result in exactly one claim (nil return) and one rejection
// (ErrStaleOwner). This proves the ON CONFLICT DO NOTHING + RowsAffected==0
// path in the cold INSERT does NOT return a KindInternal error under concurrency.
//
// ref: review finding F2.
func TestPGOwnerCheckpointStore_ColdConcurrent_SameKey(t *testing.T) {
	store, _ := newCheckpointStore(t)
	ctx := context.Background()

	const cellID = "cell-cold-concurrent"
	const projID = "proj-cold-concurrent"
	const offset = int64(1) // must be > 0 to satisfy cold-claim predicate

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []error
	)

	const goroutines = 4
	for i := range goroutines {
		token := fmt.Sprintf("token-%d", i)
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			err := store.AdvanceIfOwner(ctx, cellID, projID, tok, offset)
			mu.Lock()
			results = append(results, err)
			mu.Unlock()
		}(token)
	}
	wg.Wait()

	var nilCount, staleCount, otherCount int
	for _, err := range results {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, projection.ErrStaleOwner):
			staleCount++
		default:
			otherCount++
			t.Errorf("unexpected error (must be nil or ErrStaleOwner): %v", err)
		}
	}
	require.Equal(t, 1, nilCount, "exactly one goroutine must claim (nil return)")
	require.Equal(t, goroutines-1, staleCount, "all losers must get ErrStaleOwner, not KindInternal")
	require.Equal(t, 0, otherCount, "no unexpected errors")
}

// TestPGOwnerCheckpointStore_Concurrent_LeaderHandoff exercises the warm-update
// CAS contention path of AdvanceIfOwner under real PG concurrency. Both
// contenders use the SAME (cellID, projectionID) key, which is the case the
// previous version of this test failed to cover (it used projID+"-stale", a
// DIFFERENT key, only testing cold-offset-0 rejection, not warm-update CAS).
//
// Design:
//   - Seed: tokenA claims (cellID, projID) at offset 10 (cold claim, succeeds).
//   - Phase 1 (deterministic ordering via RunInTx): start tokenB's transaction
//     inside a goroutine. It calls AdvanceIfOwner(tokenB, 11) inside RunInTx so
//     the SELECT FOR UPDATE acquires a row lock before signalling "locked".
//     tokenB's goroutine then waits on "commit" before committing.
//   - Phase 2: main goroutine starts tokenA's attempt. tokenA calls
//     AdvanceIfOwner(tokenA, 10) which hits the same row inside its own RunInTx.
//     The SELECT FOR UPDATE BLOCKS because tokenB holds the lock — proving the
//     warm-update CAS contention point.
//   - Phase 3: signal tokenB to commit. tokenB commits: owner=tokenB, offset=11.
//     tokenA's FOR UPDATE unblocks; it reads owner=tokenB, offset=11. Since
//     tokenA != tokenB AND 10 <= 11, AdvanceIfOwner returns ErrStaleOwner via
//     the warm-update CAS (RowsAffected==0 from the SQL WHERE predicate or the
//     Go short-circuit: ownerToken != recordedOwner && offset <= recordedOffset).
//
// This proves the CAS fences at the SQL layer, not just in application logic,
// and exercises the warm-update path (row exists, SELECT FOR UPDATE blocks).
func TestPGOwnerCheckpointStore_Concurrent_LeaderHandoff(t *testing.T) {
	store, txm := newCheckpointStore(t)
	ctx := context.Background()

	const cellID = "cell-concurrent-handoff"
	const projID = "proj-concurrent-handoff"

	// Seed: tokenA claims the checkpoint at offset 10.
	const tokenA = "leader-A-established"
	const tokenB = "leader-B-new"
	require.NoError(t, store.AdvanceIfOwner(ctx, cellID, projID, tokenA, 10),
		"seed: leader A must claim at offset 10")

	// locked signals that tokenB's RunInTx has acquired the FOR UPDATE row lock.
	// commit signals tokenB to commit its transaction.
	locked := make(chan struct{})
	commit := make(chan struct{})

	var (
		errB error
		wg   sync.WaitGroup
	)

	// Goroutine: tokenB inside a RunInTx acquires the row lock and holds it
	// until "commit" is signalled, then advances to offset 11.
	wg.Add(1)
	go func() {
		defer wg.Done()
		errB = txm.RunInTx(ctx, func(txCtx context.Context) error {
			// AdvanceIfOwner inside an ambient tx: the pgexec layer routes
			// the SELECT FOR UPDATE through the tx, acquiring the row lock.
			if err := store.AdvanceIfOwner(txCtx, cellID, projID, tokenB, 11); err != nil {
				return err
			}
			// Signal the main goroutine that the row lock is held.
			close(locked)
			// Hold the lock until the main goroutine asserts tokenA is blocked.
			<-commit
			return nil
		})
	}()

	// Wait for tokenB to hold the row lock.
	select {
	case <-locked:
	case <-time.After(txHoldDuration):
		t.Fatal("tokenB did not acquire the row lock within the expected window")
	}

	// Start tokenA's attempt in a goroutine (it will block on the FOR UPDATE).
	aResult := make(chan error, 1)
	go func() {
		aResult <- txm.RunInTx(ctx, func(txCtx context.Context) error {
			return store.AdvanceIfOwner(txCtx, cellID, projID, tokenA, 10)
		})
	}()

	// Assert tokenA is blocked: it must NOT return within blockAssertTimeout
	// because tokenB holds the FOR UPDATE lock.
	select {
	case e := <-aResult:
		t.Fatalf("tokenA returned before tokenB committed (expected blocking on FOR UPDATE), got: %v", e)
	case <-time.After(blockAssertTimeout):
		// Expected: tokenA is blocked.
	}

	// Release tokenB: commit the transaction (offset=11, owner=tokenB).
	close(commit)
	wg.Wait()
	require.NoError(t, errB, "tokenB (offset 11, ahead) must succeed")

	// Now tokenA unblocks, reads owner=tokenB/offset=11, and must get ErrStaleOwner
	// via the warm-update CAS path (tokenA != tokenB AND 10 <= 11).
	select {
	case errA := <-aResult:
		assert.True(t, errors.Is(errA, projection.ErrStaleOwner),
			"tokenA (offset 10, not ahead, different owner) must get ErrStaleOwner after tokenB committed, got: %v", errA)
	case <-time.After(txHoldDuration):
		t.Fatal("tokenA did not complete after tokenB released the row lock")
	}

	// Final state: offset=11, owner=tokenB.
	finalOff, err := store.LoadOffset(ctx, cellID, projID)
	require.NoError(t, err)
	assert.Equal(t, int64(11), finalOff,
		"after handoff, committed offset must be 11 (tokenB won)")
}
