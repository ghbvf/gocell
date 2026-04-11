package redis

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface check.
var _ idempotency.Checker = (*IdempotencyChecker)(nil)

func TestIdempotencyChecker_MarkAndCheck(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// Not processed initially.
	ok, err := ic.IsProcessed(ctx, "idem:test:1")
	require.NoError(t, err)
	assert.False(t, ok)

	// Mark as processed.
	err = ic.MarkProcessed(ctx, "idem:test:1", 24*time.Hour)
	require.NoError(t, err)

	// Now should be processed.
	ok, err = ic.IsProcessed(ctx, "idem:test:1")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestIdempotencyChecker_MarkIdempotent(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// Mark twice - second should be no-op (SetNX returns false but no error).
	err := ic.MarkProcessed(ctx, "idem:test:2", 24*time.Hour)
	require.NoError(t, err)

	err = ic.MarkProcessed(ctx, "idem:test:2", 24*time.Hour)
	require.NoError(t, err)
}

func TestIdempotencyChecker_IsProcessed_GetError(t *testing.T) {
	mock := newMockCmdable()
	mock.getErr = errMock
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	ok, err := ic.IsProcessed(ctx, "idem:test:err")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
}

func TestIdempotencyChecker_MarkProcessed_SetNXError(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	err := ic.MarkProcessed(ctx, "idem:test:err", 24*time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}

func TestIdempotencyChecker_TryProcess_FirstCall(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// First call: key does not exist, should return true (caller should process).
	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try:1", 24*time.Hour)
	require.NoError(t, err)
	assert.True(t, shouldProcess, "first TryProcess should return true")

	// Verify key is now marked as processed via IsProcessed.
	ok, err := ic.IsProcessed(ctx, "idem:test:try:1")
	require.NoError(t, err)
	assert.True(t, ok, "key should be processed after TryProcess")
}

func TestIdempotencyChecker_TryProcess_Duplicate(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// First call succeeds.
	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try:2", 24*time.Hour)
	require.NoError(t, err)
	assert.True(t, shouldProcess)

	// Second call: key already exists, should return false.
	shouldProcess, err = ic.TryProcess(ctx, "idem:test:try:2", 24*time.Hour)
	require.NoError(t, err)
	assert.False(t, shouldProcess, "duplicate TryProcess should return false")
}

func TestIdempotencyChecker_TryProcess_SetNXError(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try:err", 24*time.Hour)
	require.Error(t, err)
	assert.False(t, shouldProcess)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}

func TestIdempotencyChecker_ViaClientConstructor(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})
	ic := NewIdempotencyChecker(client)
	ctx := context.Background()

	ok, err := ic.IsProcessed(ctx, "idem:test:client")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestIdempotencyChecker_MarkProcessed_ZeroTTLUsesDefault(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// TTL=0 should use DefaultTTL (24h), not create permanent keys.
	err := ic.MarkProcessed(ctx, "idem:test:zero-ttl", 0)
	require.NoError(t, err)

	// Verify the key was stored with an expiry (non-zero).
	mock.mu.Lock()
	entry, ok := mock.store["idem:test:zero-ttl"]
	mock.mu.Unlock()
	require.True(t, ok)
	assert.False(t, entry.expiry.IsZero(), "TTL=0 should use DefaultTTL, not permanent key")
}

func TestIdempotencyChecker_TryProcess_ZeroTTLUsesDefault(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try-zero-ttl", 0)
	require.NoError(t, err)
	assert.True(t, shouldProcess)

	mock.mu.Lock()
	entry, ok := mock.store["idem:test:try-zero-ttl"]
	mock.mu.Unlock()
	require.True(t, ok)
	assert.False(t, entry.expiry.IsZero(), "TTL=0 should use DefaultTTL, not permanent key")
}

func TestIdempotencyChecker_NegativeTTLUsesDefault(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	err := ic.MarkProcessed(ctx, "idem:test:neg-ttl", -5*time.Second)
	require.NoError(t, err)

	mock.mu.Lock()
	entry, ok := mock.store["idem:test:neg-ttl"]
	mock.mu.Unlock()
	require.True(t, ok)
	assert.False(t, entry.expiry.IsZero(), "negative TTL should use DefaultTTL")
}

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
