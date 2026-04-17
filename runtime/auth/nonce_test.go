package auth

import (
	"context"
	"fmt"
	"sync"
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
	store := NewInMemoryNonceStore(1*time.Second, WithNonceClock(func() time.Time { return now }))

	// Insert 1001 entries; all will expire quickly.
	for i := 0; i < 1001; i++ {
		err := store.CheckAndMark(context.Background(), fmt.Sprintf("prune-nonce-%d", i))
		require.NoError(t, err)
	}
	initialLen := len(store.seen)
	assert.GreaterOrEqual(t, initialLen, 1001)

	// Advance clock past TTL so all existing entries are expired.
	now = now.Add(2 * time.Second)

	// Insert one more entry; this triggers the lazy prune because len > 1000.
	err := store.CheckAndMark(context.Background(), "trigger-prune")
	require.NoError(t, err)

	// Map should have shrunk: only the new entry remains.
	store.mu.Lock()
	finalLen := len(store.seen)
	store.mu.Unlock()

	assert.Less(t, finalLen, initialLen, "lazy prune should have reduced map size")
	assert.Equal(t, 1, finalLen, "only the new entry should remain after prune")
}
