package auth

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const nonceD6min = 6 * time.Minute

func TestInMemoryNonceStore_FirstUseSucceeds(t *testing.T) {
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	err = store.CheckAndMark(context.Background(), "nonce-abc")
	require.NoError(t, err)
}

func TestInMemoryNonceStore_ReplayRejected(t *testing.T) {
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	err = store.CheckAndMark(context.Background(), "nonce-xyz")
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-xyz")
	assert.ErrorIs(t, err, ErrNonceReused)
}

func TestInMemoryNonceStore_DifferentNoncesSucceed(t *testing.T) {
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-1")
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-2")
	require.NoError(t, err)
}

func TestInMemoryNonceStore_ExpiredNonceAllowsReuse(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL, WithNonceClock(func() time.Time { return now }))
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-exp")
	require.NoError(t, err)

	// Advance clock past maxAge.
	now = now.Add(nonceD6min)

	err = store.CheckAndMark(context.Background(), "nonce-exp")
	require.NoError(t, err, "expired nonce should be reusable after TTL")
}

func TestInMemoryNonceStore_ConcurrentAccess(t *testing.T) {
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			nonce := fmt.Sprintf("unique-nonce-%d", i)
			err := store.CheckAndMark(context.Background(), nonce)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestInMemoryNonceStore_LazyPrune(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// Use ServiceTokenNonceTTL so the guard is satisfied, plus a low
	// maxEntries cap so the prune triggers after 10 entries.
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL,
		WithNonceClock(func() time.Time { return now }),
		WithMaxNonceEntries(10),
	)
	require.NoError(t, err)

	// Insert 10 entries; all will expire after ServiceTokenNonceTTL.
	for i := range 10 {
		err := store.CheckAndMark(context.Background(), fmt.Sprintf("prune-nonce-%d", i))
		require.NoError(t, err)
	}
	initialLen := len(store.seen)
	assert.GreaterOrEqual(t, initialLen, 10)

	// Advance clock past TTL so all existing entries are expired.
	now = now.Add(ServiceTokenNonceTTL + time.Second)

	// Insert one more entry; this triggers the lazy prune because len >= maxEntries.
	err = store.CheckAndMark(context.Background(), "trigger-prune")
	require.NoError(t, err)

	// Map should have shrunk: only the new entry remains.
	store.mu.Lock()
	finalLen := len(store.seen)
	store.mu.Unlock()

	assert.Less(t, finalLen, initialLen, "lazy prune should have reduced map size")
	assert.Equal(t, 1, finalLen, "only the new entry should remain after prune")
}

func TestInMemoryNonceStore_ConcurrentSameNonce_ExactlyOneSucceeds(t *testing.T) {
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	const goroutines = 50
	var (
		successes atomic.Int32
		failures  atomic.Int32
		wg        sync.WaitGroup
	)
	wg.Add(goroutines)
	for i := range goroutines {
		_ = i
		go func() {
			defer wg.Done()
			err := store.CheckAndMark(context.Background(), "same-nonce")
			if err == nil {
				successes.Add(1)
			} else {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), successes.Load(), "exactly one goroutine should succeed")
	assert.Equal(t, int32(goroutines-1), failures.Load(), "all others should fail")
}

func TestNewInMemoryNonceStore_ZeroMaxAge_Fails(t *testing.T) {
	_, err := NewInMemoryNonceStore(0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive")
}

func TestNewInMemoryNonceStore_NegativeMaxAge_Fails(t *testing.T) {
	_, err := NewInMemoryNonceStore(-time.Second)
	require.Error(t, err)
}

func TestInMemoryNonceStore_Kind_ReportsInMemory(t *testing.T) {
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	assert.Equal(t, NonceStoreKindInMemory, store.Kind())
}

func TestNoopNonceStore_AlwaysPermits(t *testing.T) {
	store := NewNoopNonceStore()
	// First use.
	require.NoError(t, store.CheckAndMark(context.Background(), "any-nonce"))
	// Second use of the same nonce — must still succeed (disabled replay check).
	require.NoError(t, store.CheckAndMark(context.Background(), "any-nonce"))
}

func TestNoopNonceStore_Kind_ReportsNoop(t *testing.T) {
	assert.Equal(t, NonceStoreKindNoop, NewNoopNonceStore().Kind())
}

// TestNonceStoreKind_Values pins the public constant values so downstream
// matchers (cmd/corebundle.SharedDeps.validateControlPlane) do not drift.
func TestNonceStoreKind_Values(t *testing.T) {
	assert.Equal(t, NonceStoreKind("noop"), NonceStoreKindNoop)
	assert.Equal(t, NonceStoreKind("in_memory"), NonceStoreKindInMemory)
	assert.Equal(t, NonceStoreKind("distributed"), NonceStoreKindDistributed)
}

// F5: maxAge boundary guard tests (companion to F3 NewInMemoryNonceStore guard).

func TestNewInMemoryNonceStore_MaxAgeShorterThanTokenWindow_Fails(t *testing.T) {
	_, err := NewInMemoryNonceStore(ServiceTokenNonceTTL - time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ServiceTokenNonceTTL")
}

func TestNewInMemoryNonceStore_ExactlyServiceTokenNonceTTL_Succeeds(t *testing.T) {
	_, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err, "maxAge == ServiceTokenNonceTTL must be allowed (boundary)")
}

// F4: TTL boundary test with controllable clock — verifies that a nonce is still
// protected at ServiceTokenNonceTTL-1s and reusable only after the full TTL elapses.
func TestInMemoryNonceStore_RetentionCoversServiceTokenMaxAge(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store, err := NewInMemoryNonceStore(
		ServiceTokenNonceTTL,
		WithNonceClock(func() time.Time { return now }),
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, store.CheckAndMark(ctx, "nonce-a"))

	// Critical boundary: a nonce at ServiceTokenNonceTTL-1s must still be
	// protected in the store.
	now = now.Add(ServiceTokenNonceTTL - time.Second)
	require.ErrorIs(t, store.CheckAndMark(ctx, "nonce-a"), ErrNonceReused,
		"nonce must still be protected at ServiceTokenNonceTTL-1s")

	// After full TTL, nonce may be reused.
	now = now.Add(testtime.D2s)
	require.NoError(t, store.CheckAndMark(ctx, "nonce-a"),
		"nonce must be reusable after TTL window")
}

// F8: Hard-cap enforcement — when the store is full and no entries are expired,
// CheckAndMark must return ErrNonceStoreFull instead of growing without bound.
func TestInMemoryNonceStore_MaxEntries_RejectsWhenFull(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL,
		WithNonceClock(func() time.Time { return now }),
		WithMaxNonceEntries(3))
	require.NoError(t, err)

	ctx := context.Background()
	// Fill to cap with live (unexpired) nonces.
	require.NoError(t, store.CheckAndMark(ctx, "n1"))
	require.NoError(t, store.CheckAndMark(ctx, "n2"))
	require.NoError(t, store.CheckAndMark(ctx, "n3"))

	// Next insert must fail — prune finds nothing expired, cap reached.
	err = store.CheckAndMark(ctx, "n4")
	require.ErrorIs(t, err, ErrNonceStoreFull,
		"store at cap with no expired entries must return ErrNonceStoreFull")
}

// F6: MaxAge accessor test — confirms the configured retention window is readable.
func TestInMemoryNonceStore_MaxAge_ReportsConfiguredDuration(t *testing.T) {
	want := ServiceTokenNonceTTL
	store, err := NewInMemoryNonceStore(want)
	require.NoError(t, err)
	assert.Equal(t, want, store.MaxAge(), "MaxAge() must report the configured retention window")
}
