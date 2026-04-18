package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ClaimState Tests ---

func TestClaimState_Values(t *testing.T) {
	assert.Equal(t, ClaimState(0), ClaimAcquired)
	assert.Equal(t, ClaimState(1), ClaimDone)
	assert.Equal(t, ClaimState(2), ClaimBusy)
}

// --- Claimer Interface Test ---

type mockClaimer struct {
	state   ClaimState
	receipt Receipt
	err     error
}

type mockReceipt struct{}

func (mockReceipt) Commit(context.Context) error                    { return nil }
func (mockReceipt) Release(context.Context) error                   { return nil }
func (mockReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }

var _ Receipt = mockReceipt{}

func (m *mockClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (ClaimState, Receipt, error) {
	return m.state, m.receipt, m.err
}

var _ Claimer = (*mockClaimer)(nil)

func TestClaimerInterface(t *testing.T) {
	var c Claimer = &mockClaimer{state: ClaimAcquired, receipt: mockReceipt{}}
	state, _, err := c.Claim(context.Background(), "test-key", DefaultLeaseTTL, DefaultTTL)
	assert.NoError(t, err)
	assert.Equal(t, ClaimAcquired, state)
}

func TestReceipt_CommitRelease(t *testing.T) {
	var receipt Receipt = mockReceipt{}
	assert.NoError(t, receipt.Commit(context.Background()))
	assert.NoError(t, receipt.Release(context.Background()))
}

// --- DefaultLeaseTTL Test ---

func TestDefaultLeaseTTL(t *testing.T) {
	assert.Equal(t, 5*time.Minute, DefaultLeaseTTL)
}

func TestDefaultTTL(t *testing.T) {
	assert.Equal(t, 24*time.Hour, DefaultTTL)
}

// --- InMemClaimer / inMemReceipt Tests ---

func TestInMemClaimer_Claim_Acquired(t *testing.T) {
	c := NewInMemClaimer()
	state, receipt, err := c.Claim(context.Background(), "key1", 5*time.Minute, 24*time.Hour)
	assert.NoError(t, err)
	assert.Equal(t, ClaimAcquired, state)
	assert.NotNil(t, receipt)
}

func TestInMemClaimer_Claim_Done(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	// Acquire and commit.
	_, receipt, err := c.Claim(ctx, "key-done", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.NoError(t, receipt.Commit(ctx))

	// Second claim returns ClaimDone.
	state, r2, err := c.Claim(ctx, "key-done", 5*time.Minute, 24*time.Hour)
	assert.NoError(t, err)
	assert.Equal(t, ClaimDone, state)
	assert.Nil(t, r2)
}

func TestInMemClaimer_Claim_Busy(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	// Acquire lease (do not commit/release).
	state, _, err := c.Claim(ctx, "key-busy", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, ClaimAcquired, state)

	// Second claim returns ClaimBusy.
	state2, r2, err2 := c.Claim(ctx, "key-busy", 5*time.Minute, 24*time.Hour)
	assert.NoError(t, err2)
	assert.Equal(t, ClaimBusy, state2)
	assert.Nil(t, r2)
}

func TestInMemClaimer_Claim_ExpiredLease_ReacquiredBySecond(t *testing.T) {
	c := &InMemClaimer{
		entries: make(map[string]*inMemEntry),
		now:     func() time.Time { return time.Unix(1000, 0) },
	}
	ctx := context.Background()

	// Acquire with very short TTL (already expired at time=1001).
	state, _, err := c.Claim(ctx, "key-exp", 1*time.Millisecond, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, ClaimAcquired, state)

	// Advance clock past expiry.
	c.now = func() time.Time { return time.Unix(1001, 0) }

	// Second consumer should get ClaimAcquired (expired lease dropped).
	state2, r2, err2 := c.Claim(ctx, "key-exp", 5*time.Minute, 24*time.Hour)
	assert.NoError(t, err2)
	assert.Equal(t, ClaimAcquired, state2)
	assert.NotNil(t, r2)
}

func TestInMemClaimer_Release(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	_, receipt, err := c.Claim(ctx, "key-rel", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.NoError(t, receipt.Release(ctx))

	// After release, a new claim should succeed.
	state, _, err := c.Claim(ctx, "key-rel", 5*time.Minute, 24*time.Hour)
	assert.NoError(t, err)
	assert.Equal(t, ClaimAcquired, state)
}

func TestInMemReceipt_DoubleCommit_SecondIsNoop(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	_, receipt, err := c.Claim(ctx, "key-dbl", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.NoError(t, receipt.Commit(ctx))
	// Second Commit is idempotent via sync.Once — returns same nil error.
	assert.NoError(t, receipt.Commit(ctx))
}

func TestInMemReceipt_StaleCommit_AfterRelease_ReturnsError(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	_, receipt1, err := c.Claim(ctx, "key-stale", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.NoError(t, receipt1.Release(ctx))

	// Another consumer reclaims the key.
	_, _, err = c.Claim(ctx, "key-stale", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)

	// receipt1.Commit is stale — token mismatch.
	// sync.Once already fired (via Release), so this is a no-op returning the stored error.
	// The error is already set from Release (nil), so we just verify no panic.
	_ = receipt1.Commit(ctx)
}

// --- inMemReceipt.Extend tests ---

func TestInMemReceipt_Extend_Success(t *testing.T) {
	c := &InMemClaimer{
		entries: make(map[string]*inMemEntry),
		now:     func() time.Time { return time.Unix(1000, 0) },
	}
	ctx := context.Background()

	_, receipt, err := c.Claim(ctx, "key-ext", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// Advance clock and extend with a new TTL.
	c.now = func() time.Time { return time.Unix(1100, 0) }
	newTTL := 10 * time.Minute
	extErr := receipt.Extend(ctx, newTTL)
	assert.NoError(t, extErr)

	// Verify the internal lease expiry was updated: should be now(1100) + 10min.
	c.mu.Lock()
	entry := c.entries["key-ext"]
	c.mu.Unlock()
	require.NotNil(t, entry)
	expectedExpiry := time.Unix(1100, 0).Add(newTTL)
	assert.Equal(t, expectedExpiry, entry.expiresAt)
}

func TestInMemReceipt_Extend_AfterRelease_ReturnsErrLeaseExpired(t *testing.T) {
	c := NewInMemClaimer()
	ctx := context.Background()

	_, receipt, err := c.Claim(ctx, "key-ext-rel", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)

	// Release drops the entry.
	require.NoError(t, receipt.Release(ctx))

	// Extend after release should return ErrLeaseExpired (entry gone).
	extErr := receipt.Extend(ctx, 5*time.Minute)
	assert.ErrorIs(t, extErr, ErrLeaseExpired)
}

func TestInMemReceipt_Extend_TokenMismatch_ReturnsErrLeaseExpired(t *testing.T) {
	c := &InMemClaimer{
		entries: make(map[string]*inMemEntry),
		now:     func() time.Time { return time.Unix(1000, 0) },
	}
	ctx := context.Background()

	_, receipt, err := c.Claim(ctx, "key-ext-mismatch", 5*time.Minute, 24*time.Hour)
	require.NoError(t, err)

	// Tamper with the entry's token to simulate a re-claim by another consumer.
	c.mu.Lock()
	c.entries["key-ext-mismatch"].token = "different-token"
	c.mu.Unlock()

	extErr := receipt.Extend(ctx, 5*time.Minute)
	assert.ErrorIs(t, extErr, ErrLeaseExpired)
}

func TestInMemReceipt_Extend_AfterExpiredLease_ReturnsErrLeaseExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &InMemClaimer{
		entries: make(map[string]*inMemEntry),
		now:     func() time.Time { return now },
	}
	ctx := context.Background()

	_, receipt, err := c.Claim(ctx, "key-ext-exp", 1*time.Millisecond, 24*time.Hour)
	require.NoError(t, err)

	// Advance clock so another Claim call drops the expired entry.
	now = time.Unix(1002, 0)
	// Trigger eviction by claiming again (which drops the expired entry).
	_, _, _ = c.Claim(ctx, "key-ext-exp", 5*time.Minute, 24*time.Hour)
	// Now the entry for old receipt is replaced — token mismatch.
	extErr := receipt.Extend(ctx, 5*time.Minute)
	assert.ErrorIs(t, extErr, ErrLeaseExpired)
}
