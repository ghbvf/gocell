package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// IdempotencyClaimer Tests (Solution B two-phase model)
// =============================================================================

// Compile-time interface check for the new Claimer.
var _ idempotency.Claimer = (*IdempotencyClaimer)(nil)

func TestIdempotencyClaimer_Claim_Acquired(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:claim:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
	assert.NotNil(t, receipt)

	// Verify the lease key was set.
	mock.mu.Lock()
	_, hasLease := mock.store["lease:idem:claim:1"]
	mock.mu.Unlock()
	assert.True(t, hasLease)
}

func TestIdempotencyClaimer_Claim_Done(t *testing.T) {
	mock := newClaimerMock()
	ctx := context.Background()

	// Pre-set the done key to simulate a previously completed processing.
	mock.mu.Lock()
	mock.store["done:idem:claim:2"] = mockEntry{value: "1"}
	mock.mu.Unlock()

	claimer := newIdempotencyClaimerFromCmdable(mock)
	state, receipt, err := claimer.Claim(ctx, "idem:claim:2", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimDone, state)
	assert.Nil(t, receipt)
}

func TestIdempotencyClaimer_Claim_Busy(t *testing.T) {
	mock := newClaimerMock()
	ctx := context.Background()

	// Pre-set the lease key to simulate another consumer processing.
	mock.mu.Lock()
	mock.store["lease:idem:claim:3"] = mockEntry{value: "other-token", expiry: time.Now().Add(5 * time.Minute)}
	mock.mu.Unlock()

	claimer := newIdempotencyClaimerFromCmdable(mock)
	state, receipt, err := claimer.Claim(ctx, "idem:claim:3", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimBusy, state)
	assert.Nil(t, receipt)
}

func TestIdempotencyClaimer_Receipt_Commit(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:commit:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Commit should set done key and remove lease key.
	err = receipt.Commit(ctx)
	require.NoError(t, err)

	mock.mu.Lock()
	_, hasLease := mock.store["lease:idem:commit:1"]
	doneEntry, hasDone := mock.store["done:idem:commit:1"]
	mock.mu.Unlock()

	assert.False(t, hasLease, "lease key should be deleted after commit")
	assert.True(t, hasDone, "done key should exist after commit")
	assert.Equal(t, "1", doneEntry.value)
}

func TestIdempotencyClaimer_Receipt_Release(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:release:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Release should remove lease key without setting done.
	err = receipt.Release(ctx)
	require.NoError(t, err)

	mock.mu.Lock()
	_, hasLease := mock.store["lease:idem:release:1"]
	_, hasDone := mock.store["done:idem:release:1"]
	mock.mu.Unlock()

	assert.False(t, hasLease, "lease key should be deleted after release")
	assert.False(t, hasDone, "done key should NOT exist after release")
}

func TestIdempotencyClaimer_After_Commit_Claim_Returns_Done(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	// First claim.
	state, receipt, err := claimer.Claim(ctx, "idem:full:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)

	// Commit.
	err = receipt.Commit(ctx)
	require.NoError(t, err)

	// Second claim should return ClaimDone.
	state, receipt2, err := claimer.Claim(ctx, "idem:full:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimDone, state)
	assert.Nil(t, receipt2)
}

func TestIdempotencyClaimer_After_Release_Claim_Reacquires(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	// First claim.
	state, receipt, err := claimer.Claim(ctx, "idem:reacq:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)

	// Release.
	err = receipt.Release(ctx)
	require.NoError(t, err)

	// Second claim should re-acquire (not Done or Busy).
	state, receipt2, err := claimer.Claim(ctx, "idem:reacq:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
	assert.NotNil(t, receipt2)
}

func TestIdempotencyClaimer_Claim_DefaultTTL(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	// Pass zero TTLs — should use defaults.
	state, receipt, err := claimer.Claim(ctx, "idem:default-ttl:1", 0, 0)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
	assert.NotNil(t, receipt)
}

func TestIdempotencyClaimer_Claim_EvalError(t *testing.T) {
	mock := newClaimerMock()
	mock.evalErr = errMock
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:err:1", 5*time.Minute, 24*time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
	assert.Equal(t, idempotency.ClaimState(0), state)
	assert.Nil(t, receipt)
}

func TestIdempotencyClaimer_Receipt_Commit_StaleToken(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:stale-commit:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Simulate lease expiry by deleting the lease key from the store.
	mock.mu.Lock()
	delete(mock.store, "lease:idem:stale-commit:1")
	mock.mu.Unlock()

	// Commit should fail with stale lease error.
	err = receipt.Commit(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
	assert.Contains(t, err.Error(), "stale lease")
}

func TestIdempotencyClaimer_Receipt_Release_StaleToken(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:stale-release:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Simulate lease expiry by deleting the lease key from the store.
	mock.mu.Lock()
	delete(mock.store, "lease:idem:stale-release:1")
	mock.mu.Unlock()

	// Release should fail with stale lease error.
	err = receipt.Release(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_DELETE")
	assert.Contains(t, err.Error(), "stale lease")
}

func TestIdempotencyClaimer_ViaClientConstructor(t *testing.T) {
	mock := newClaimerMock()
	client := newClientFromCmdable(mock, Config{})
	claimer := NewIdempotencyClaimer(client)
	ctx := context.Background()

	state, _, err := claimer.Claim(ctx, "idem:client:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
}

func TestIdempotencyClaimer_Receipt_DoubleCommit_Idempotent(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:double-commit:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// First Commit should succeed.
	err = receipt.Commit(ctx)
	require.NoError(t, err)

	// Second Commit should be a no-op (not "stale lease" error).
	err = receipt.Commit(ctx)
	require.NoError(t, err)
}

func TestIdempotencyClaimer_Receipt_DoubleRelease_Idempotent(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:double-release:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// First Release should succeed.
	err = receipt.Release(ctx)
	require.NoError(t, err)

	// Second Release should be a no-op (not "stale lease" error).
	err = receipt.Release(ctx)
	require.NoError(t, err)
}

func TestIdempotencyClaimer_Receipt_DoubleCommit_ErrorCached(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:double-commit-err:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Delete the lease key to make Commit fail with stale token error.
	mock.mu.Lock()
	delete(mock.store, "lease:idem:double-commit-err:1")
	mock.mu.Unlock()

	// First Commit should fail.
	err1 := receipt.Commit(ctx)
	require.Error(t, err1)
	assert.Contains(t, err1.Error(), "stale lease")

	// Second Commit should return the SAME cached error (committed/released guard under mu).
	err2 := receipt.Commit(ctx)
	require.Error(t, err2)
	assert.Equal(t, err1, err2, "repeated Commit must return the same cached error")
}

func TestIdempotencyClaimer_Receipt_DoubleRelease_ErrorCached(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	state, receipt, err := claimer.Claim(ctx, "idem:double-release-err:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Delete the lease key to make Release fail with stale token error.
	mock.mu.Lock()
	delete(mock.store, "lease:idem:double-release-err:1")
	mock.mu.Unlock()

	// First Release should fail.
	err1 := receipt.Release(ctx)
	require.Error(t, err1)
	assert.Contains(t, err1.Error(), "stale lease")

	// Second Release should return the SAME cached error (committed/released guard under mu).
	err2 := receipt.Release(ctx)
	require.Error(t, err2)
	assert.Equal(t, err1, err2, "repeated Release must return the same cached error")
}

func TestIdempotencyClaimer_Claim_Concurrent_OneAcquiredOneBusy(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	type result struct {
		state   idempotency.ClaimState
		receipt interface{} // non-nil check only
		err     error
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	for range 2 {
		go func() {
			defer wg.Done()
			state, receipt, err := claimer.Claim(ctx, "idem:concurrent:1", 5*time.Minute, 24*time.Hour)
			results <- result{state: state, receipt: receipt, err: err}
		}()
	}

	wg.Wait()
	close(results)

	var acquired, busy int
	for r := range results {
		require.NoError(t, r.err)
		switch r.state {
		case idempotency.ClaimAcquired:
			acquired++
			assert.NotNil(t, r.receipt, "ClaimAcquired must return a non-nil receipt")
		case idempotency.ClaimBusy:
			busy++
			assert.Nil(t, r.receipt, "ClaimBusy must return nil receipt")
		default:
			t.Fatalf("unexpected ClaimState %d", r.state)
		}
	}

	assert.Equal(t, 1, acquired, "exactly one goroutine should acquire the lease")
	assert.Equal(t, 1, busy, "exactly one goroutine should get ClaimBusy")
}

func TestIdempotencyClaimer_Receipt_Commit_TransientError_ThenRetrySuccess(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	// Claim a key successfully.
	state, receipt, err := claimer.Claim(ctx, "idem:transient:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// First Commit: inject a transient Redis error.
	mock.evalErr = errMock
	err = receipt.Commit(ctx)
	require.Error(t, err, "first Commit should fail due to transient error")
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")

	// Clear the transient error — Redis has recovered.
	mock.evalErr = nil

	// Second Commit: should succeed (committed=false allows retry).
	err = receipt.Commit(ctx)
	require.NoError(t, err, "second Commit should succeed after transient error clears")

	// Third Commit: should be a no-op (committed=true, returns nil).
	err = receipt.Commit(ctx)
	require.NoError(t, err, "third Commit should be no-op")

	// Verify done key exists and lease key is removed.
	mock.mu.Lock()
	_, hasLease := mock.store["lease:idem:transient:1"]
	_, hasDone := mock.store["done:idem:transient:1"]
	mock.mu.Unlock()
	assert.False(t, hasLease, "lease key should be deleted after successful commit")
	assert.True(t, hasDone, "done key should exist after successful commit")
}

// =============================================================================
// Receipt.Extend tests
// =============================================================================

func TestReceipt_Extend_Success(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	_, receipt, err := claimer.Claim(ctx, "idem:extend:1", 5*time.Second, 24*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// Extend should succeed and update the TTL.
	err = receipt.Extend(ctx, 10*time.Second)
	require.NoError(t, err)

	// The lease key must still exist with updated expiry.
	mock.mu.Lock()
	entry, hasLease := mock.store["lease:idem:extend:1"]
	mock.mu.Unlock()

	require.True(t, hasLease, "lease key should still exist after Extend")
	// Expiry should be approximately 10s from now (within 1s tolerance).
	assert.WithinDuration(t, time.Now().Add(10*time.Second), entry.expiry, time.Second)
}

func TestReceipt_Extend_LeaseExpired(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	_, receipt, err := claimer.Claim(ctx, "idem:extend:2", 5*time.Second, 24*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// Simulate lease taken by another consumer — delete the lease key.
	mock.mu.Lock()
	delete(mock.store, "lease:idem:extend:2")
	mock.mu.Unlock()

	// Extend on a lost lease must return ErrLeaseExpired.
	err = receipt.Extend(ctx, 10*time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, idempotency.ErrLeaseExpired)
}

func TestReceipt_Extend_BackendDown(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	_, receipt, err := claimer.Claim(ctx, "idem:extend:3", 5*time.Second, 24*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// Inject a backend error.
	mock.evalErr = errMock

	// Extend should wrap the error but NOT classify it as ErrLeaseExpired.
	err = receipt.Extend(ctx, 10*time.Second)
	require.Error(t, err)
	assert.NotErrorIs(t, err, idempotency.ErrLeaseExpired, "backend error should not be ErrLeaseExpired")
}

func TestIdempotencyClaimer_Receipt_Release_TransientError_ThenRetrySuccess(t *testing.T) {
	mock := newClaimerMock()
	claimer := newIdempotencyClaimerFromCmdable(mock)
	ctx := context.Background()

	// Claim a key successfully.
	state, receipt, err := claimer.Claim(ctx, "idem:transient-rel:1", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// First Release: inject a transient Redis error.
	mock.evalErr = errMock
	err = receipt.Release(ctx)
	require.Error(t, err, "first Release should fail due to transient error")
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_DELETE")

	// Clear the transient error — Redis has recovered.
	mock.evalErr = nil

	// Second Release: should succeed (released=false allows retry).
	err = receipt.Release(ctx)
	require.NoError(t, err, "second Release should succeed after transient error clears")

	// Third Release: should be a no-op (released=true, returns nil).
	err = receipt.Release(ctx)
	require.NoError(t, err, "third Release should be no-op")

	// Verify lease key is removed and done key does NOT exist.
	mock.mu.Lock()
	_, hasLease := mock.store["lease:idem:transient-rel:1"]
	_, hasDone := mock.store["done:idem:transient-rel:1"]
	mock.mu.Unlock()
	assert.False(t, hasLease, "lease key should be deleted after successful release")
	assert.False(t, hasDone, "done key should NOT exist after release")
}
