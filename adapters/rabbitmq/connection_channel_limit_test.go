package rabbitmq

// Tests for Connection.AcquireChannel MaxChannelsPerConn cap (Batch B).
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: rabbitmq/amqp091-go connection.go openTune — broker channel_max negotiation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// newTestConnectionWithCap constructs a Connection with a specific
// MaxChannelsPerConn and ChannelPoolSize for channel-limit tests.
// The returned *mockConnection is nil when the caller does not need to
// inject custom channel sequences — use newTestConnectionWithCapAndMock
// when the test needs to control the mock directly.
func newTestConnectionWithCap(t *testing.T, poolSize, maxChannels int) *Connection {
	t.Helper()
	conn, _ := newTestConnectionWithCapAndMock(t, poolSize, maxChannels)
	return conn
}

// newTestConnectionWithCapAndMock is like newTestConnectionWithCap but also
// returns the underlying *mockConnection so tests can inject channel queues.
func newTestConnectionWithCapAndMock(t *testing.T, poolSize, maxChannels int) (*Connection, *mockConnection) {
	t.Helper()
	mockConn := newMockConnection()

	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	conn, err := NewConnection(Config{
		URL:                testAMQPURL,
		ChannelPoolSize:    poolSize,
		ConfirmTimeout:     testtime.D2s,
		MaxChannelsPerConn: maxChannels,
	}, WithDialFunc(dialFunc), WithConnectionClock(clock.Real()))
	require.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer cancel()
		if cErr := conn.Close(ctx); cErr != nil {
			t.Logf("cleanup close error: %v", cErr)
		}
	})

	return conn, mockConn
}

// TestAcquireChannel_RejectsWhenAtMax verifies that the third pool-miss
// AcquireChannel call returns ErrAdapterAMQPChannelMaxExceeded when
// MaxChannelsPerConn=2.
func TestAcquireChannel_RejectsWhenAtMax(t *testing.T) {
	t.Parallel()

	// poolSize=0 forces every acquire to be a pool miss.
	conn := newTestConnectionWithCap(t, 0, 2)

	ch1, err := conn.AcquireChannel()
	require.NoError(t, err, "first acquire must succeed")
	require.NotNil(t, ch1)

	ch2, err := conn.AcquireChannel()
	require.NoError(t, err, "second acquire must succeed")
	require.NotNil(t, ch2)

	// Third acquire must be rejected — cap reached.
	ch3, err := conn.AcquireChannel()
	assert.Nil(t, ch3, "channel must be nil when cap is exceeded")
	require.Error(t, err, "third acquire must return an error")

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr, "error must be an errcode.Error")
	assert.Equal(t, ErrAdapterAMQPChannelMaxExceeded, ecErr.Code,
		"error code must be ErrAdapterAMQPChannelMaxExceeded")

	// Counter must stay at 2 (rolled back the failed attempt).
	assert.Equal(t, int32(2), conn.inUseChannels.Load(),
		"inUseChannels must remain at cap after rejected acquire")
}

// TestAcquireChannel_PoolHitDoesNotIncrement verifies that acquiring a channel
// from the pool (pool hit) does not increment inUseChannels — the broker
// channel slot was already counted on initial allocation.
func TestAcquireChannel_PoolHitDoesNotIncrement(t *testing.T) {
	t.Parallel()

	// poolSize=2, maxChannels=2 — we seed 1 channel into the pool.
	conn := newTestConnectionWithCap(t, 2, 2)

	// Seed a channel directly into the pool to simulate an already-allocated
	// idle channel. inUseChannels is intentionally left at 0 to assert that
	// a pool hit does not increment it.
	poolCh := newMockChannel()
	conn.channelPool <- poolCh

	// Pool hit — should return the seeded channel without changing the counter.
	ch, err := conn.AcquireChannel()
	require.NoError(t, err, "pool-hit acquire must succeed")
	assert.Equal(t, AMQPChannel(poolCh), ch, "must return the seeded pool channel")

	// Counter must stay at 0 — the pool-hit path does not touch inUseChannels.
	assert.Equal(t, int32(0), conn.inUseChannels.Load(),
		"pool-hit must not increment inUseChannels")
}

// TestReleaseChannel_DecrementsInUseOnClose verifies that when the pool is
// full and a released channel is closed instead of returned, inUseChannels
// is decremented.
//
// Invariant recap:
//   - pool-miss acquire:   inUseChannels +1
//   - pool-hit acquire:    inUseChannels unchanged
//   - pool-return release: inUseChannels unchanged (channel lives in idle pool)
//   - pool-full release:   inUseChannels -1 (channel destroyed)
//
// Strategy:
//  1. pool-miss acquire ch1 → inUseChannels=1; release ch1 into pool → pool has 1 idle.
//  2. pool-hit acquire ch1 back (pool-hit, inUseChannels=1); pool is now empty.
//  3. pool-miss acquire ch2 → inUseChannels=2.
//  4. Release ch1 into empty pool → inUseChannels=2, pool has 1 idle.
//  5. Release ch2 — pool is full → close path → inUseChannels=1.
func TestReleaseChannel_DecrementsInUseOnClose(t *testing.T) {
	t.Parallel()

	// poolSize=1, maxChannels=4.
	conn, mockConn2 := newTestConnectionWithCapAndMock(t, 1, 4)

	// Step 1: pool-miss acquire ch1 → inUseChannels=1; release into pool.
	ch1, err := conn.AcquireChannel()
	require.NoError(t, err, "ch1 pool-miss acquire must succeed")
	conn.ReleaseChannel(ch1) // pool: 1 idle, inUseChannels=1

	// Step 2: pool-hit acquire ch1 back; pool becomes empty. inUseChannels=1.
	ch1Back, err := conn.AcquireChannel()
	require.NoError(t, err, "ch1 pool-hit re-acquire must succeed")
	assert.Equal(t, int32(1), conn.inUseChannels.Load(),
		"inUseChannels unchanged after pool-hit")

	// Step 3: pool is empty → pool-miss acquire ch2 → inUseChannels=2.
	ch2 := newMockChannel()
	mockConn2.mu.Lock()
	mockConn2.nextCh = ch2
	mockConn2.mu.Unlock()

	acquiredCh2, err := conn.AcquireChannel()
	require.NoError(t, err, "ch2 pool-miss acquire must succeed")
	assert.Equal(t, int32(2), conn.inUseChannels.Load(),
		"inUseChannels must be 2 after ch2 pool-miss acquire")

	// Step 4: release ch1 into empty pool. pool: 1 idle. inUseChannels=2.
	conn.ReleaseChannel(ch1Back)
	assert.Equal(t, int32(2), conn.inUseChannels.Load(),
		"inUseChannels unchanged after ch1 pool-return")

	// Step 5: release ch2 — pool is full → close path → inUseChannels=1.
	conn.ReleaseChannel(acquiredCh2)
	assert.Equal(t, int32(1), conn.inUseChannels.Load(),
		"inUseChannels must be decremented to 1 after pool-full close path")

	// ch2 must have been closed on the pool-full path.
	assert.True(t, ch2.closeCalled,
		"ch2 must be closed when pool is full on release")
}

// TestAcquireChannel_RaceUnderConcurrency runs N goroutines concurrently
// acquiring channels and asserts that the total successful acquisitions
// never exceed MaxChannelsPerConn. Uses -race to detect data races.
func TestAcquireChannel_RaceUnderConcurrency(t *testing.T) {
	t.Parallel()

	const maxChannels = 8
	const goroutines = 32

	// poolSize=0 forces every goroutine to go through the pool-miss path.
	conn := newTestConnectionWithCap(t, 0, maxChannels)

	var (
		wg      sync.WaitGroup
		success atomic.Int32
	)

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			ch, err := conn.AcquireChannel()
			if err == nil && ch != nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()

	got := success.Load()
	assert.LessOrEqual(t, int(got), maxChannels,
		"successful acquisitions (%d) must not exceed MaxChannelsPerConn (%d)", got, maxChannels)
	assert.Equal(t, got, conn.inUseChannels.Load(),
		"inUseChannels must match successful acquisition count")
}
