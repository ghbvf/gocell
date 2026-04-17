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
)

func TestInMemoryNonceStore_FirstUseSucceeds(t *testing.T) {
	store := NewInMemoryNonceStore(5 * time.Minute)
	err := store.CheckAndMark(context.Background(), "nonce-abc")
	require.NoError(t, err)
}

func TestInMemoryNonceStore_ReplayRejected(t *testing.T) {
	store := NewInMemoryNonceStore(5 * time.Minute)
	err := store.CheckAndMark(context.Background(), "nonce-xyz")
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-xyz")
	assert.ErrorIs(t, err, ErrNonceReused)
}

func TestInMemoryNonceStore_DifferentNoncesSucceed(t *testing.T) {
	store := NewInMemoryNonceStore(5 * time.Minute)

	err := store.CheckAndMark(context.Background(), "nonce-1")
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-2")
	require.NoError(t, err)
}

func TestInMemoryNonceStore_ExpiredNonceAllowsReuse(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store := NewInMemoryNonceStore(5*time.Minute, WithNonceClock(func() time.Time { return now }))

	err := store.CheckAndMark(context.Background(), "nonce-exp")
	require.NoError(t, err)

	// Advance clock past maxAge.
	now = now.Add(6 * time.Minute)

	err = store.CheckAndMark(context.Background(), "nonce-exp")
	require.NoError(t, err, "expired nonce should be reusable after TTL")
}

func TestInMemoryNonceStore_ConcurrentAccess(t *testing.T) {
	store := NewInMemoryNonceStore(5 * time.Minute)
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
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
	// Use a low maxEntries cap so the prune triggers after 10 entries.
	store := NewInMemoryNonceStore(1*time.Second,
		WithNonceClock(func() time.Time { return now }),
		WithMaxNonceEntries(10),
	)

	// Insert 10 entries; all will expire quickly.
	for i := 0; i < 10; i++ {
		err := store.CheckAndMark(context.Background(), fmt.Sprintf("prune-nonce-%d", i))
		require.NoError(t, err)
	}
	initialLen := len(store.seen)
	assert.GreaterOrEqual(t, initialLen, 10)

	// Advance clock past TTL so all existing entries are expired.
	now = now.Add(2 * time.Second)

	// Insert one more entry; this triggers the lazy prune because len >= maxEntries.
	err := store.CheckAndMark(context.Background(), "trigger-prune")
	require.NoError(t, err)

	// Map should have shrunk: only the new entry remains.
	store.mu.Lock()
	finalLen := len(store.seen)
	store.mu.Unlock()

	assert.Less(t, finalLen, initialLen, "lazy prune should have reduced map size")
	assert.Equal(t, 1, finalLen, "only the new entry should remain after prune")
}

func TestInMemoryNonceStore_ConcurrentSameNonce_ExactlyOneSucceeds(t *testing.T) {
	store := NewInMemoryNonceStore(5 * time.Minute)
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
