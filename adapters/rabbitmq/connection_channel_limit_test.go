package rabbitmq

// Tests for Connection.AcquireChannel MaxChannelsPerConn cap (Batch B).
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: rabbitmq/amqp091-go connection.go openTune — broker channel_max negotiation

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
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

// TestPublisher_RepeatedPublish_DoesNotLeakInUseChannels verifies that each
// Publish call leaves inUseChannels at 0 after completion — i.e. the
// CloseEphemeralChannel defer correctly rolls back the pool-miss increment.
//
// Without the fix (bare ch.Close() instead of CloseEphemeralChannel), every
// Publish permanently increments inUseChannels and the 4th publish would hit
// ErrAdapterAMQPChannelMaxExceeded with MaxChannelsPerConn=3.
func TestPublisher_RepeatedPublish_DoesNotLeakInUseChannels(t *testing.T) {
	// Must NOT be t.Parallel() — this test reads conn.inUseChannels between
	// publishes and requires no concurrent modify.
	const numPublishes = 5
	const maxChannels = 3

	// Build a connection with MaxChannelsPerConn=3 and poolSize=0 to force
	// every Publish through the pool-miss path.
	conn, mockConn := newTestConnectionWithCapAndMock(t, 0, maxChannels)

	// Pre-populate channelQueue with numPublishes autoConfirm channels.
	// Publisher creates a new channel per Publish; the queue supplies them
	// in FIFO order so each call gets its own distinct channel instance.
	mockConn.mu.Lock()
	for range numPublishes {
		mockConn.channelQueue = append(mockConn.channelQueue, newAutoConfirmChannel())
	}
	mockConn.mu.Unlock()

	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))

	for i := range numPublishes {
		err := pub.Publish(context.Background(), "test.topic", []byte(`{}`))
		require.NoError(t, err, "publish %d must succeed", i+1)

		// After each Publish the channel must have been released; inUseChannels
		// should be back at 0.
		require.Equal(t, int32(0), conn.inUseChannels.Load(),
			"inUseChannels must be 0 after publish %d (channel leak detected)", i+1)
	}
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

// TestReconnect_DrainPool_ReleasesInUseChannels verifies that drainChannelPool,
// called during reconnect, decrements inUseChannels for each idle pool channel.
//
// Before the P1 fix, drainChannelPool called ch.Close() directly without
// decrementing inUseChannels, permanently leaking the cap slot. After
// maxChannels channels were cycled through (acquire → release → reconnect),
// the pool would be "full" with inUseChannels at cap, causing every subsequent
// AcquireChannel to return ErrAdapterAMQPChannelMaxExceeded ("fake full").
func TestReconnect_DrainPool_ReleasesInUseChannels(t *testing.T) {
	const poolSize = 3
	const maxChannels = 3

	// Two-connection dial sequence: initial + reconnect.
	var dialMu sync.Mutex
	var dialCount int
	mocks := []*mockConnection{newMockConnection(), newMockConnection()}
	dialFunc := func(url string) (AMQPConnection, error) {
		dialMu.Lock()
		dialCount++
		n := dialCount
		dialMu.Unlock()
		if n <= len(mocks) {
			return mocks[n-1], nil
		}
		return newMockConnection(), nil
	}

	conn, err := NewConnection(Config{
		URL:                testAMQPURL,
		ChannelPoolSize:    poolSize,
		MaxChannelsPerConn: maxChannels,
		ReconnectBaseDelay: testtime.D1ms, // fast reconnect for test
		ConfirmTimeout:     testtime.D2s,
	}, WithDialFunc(dialFunc), WithConnectionClock(clock.Real()))
	require.NoError(t, err)
	defer func() {
		if cErr := conn.Close(context.Background()); cErr != nil {
			t.Logf("cleanup close: %v", cErr)
		}
	}()

	// Acquire 3 channels (fills inUseChannels to maxChannels=3).
	ch1, err := conn.AcquireChannel()
	require.NoError(t, err)
	ch2, err := conn.AcquireChannel()
	require.NoError(t, err)
	ch3, err := conn.AcquireChannel()
	require.NoError(t, err)

	assert.Equal(t, int32(3), conn.inUseChannels.Load(), "inUseChannels must be 3 after acquiring 3 channels")

	// Release all 3 back to pool (pool-return path: inUseChannels stays at 3).
	conn.ReleaseChannel(ch1)
	conn.ReleaseChannel(ch2)
	conn.ReleaseChannel(ch3)

	assert.Equal(t, int32(3), conn.inUseChannels.Load(), "inUseChannels must stay at 3 after pool-return releases")

	// Trigger reconnect by sending a close notification on mock1.
	require.Eventually(t, func() bool {
		mocks[0].mu.Lock()
		defer mocks[0].mu.Unlock()
		return mocks[0].notifyCloseCh != nil
	}, testtime.D2s, testtime.D10ms, "reconnectLoop must have registered NotifyClose")

	mocks[0].mu.Lock()
	notifyCh := mocks[0].notifyCloseCh
	mocks[0].isClosed = true
	mocks[0].mu.Unlock()
	notifyCh <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait for reconnect to complete (dial count reaches 2).
	require.Eventually(t, func() bool {
		dialMu.Lock()
		defer dialMu.Unlock()
		return dialCount >= 2
	}, testtime.D2s, testtime.D10ms, "reconnect must complete within 2s")

	// Wait for the reconnect loop to re-enter StateConnected (so drainChannelPool
	// has been called and inUseChannels has been decremented).
	require.Eventually(t, func() bool {
		return conn.Health(context.Background()) == nil
	}, testtime.D2s, testtime.D10ms, "connection must be healthy after reconnect")

	// inUseChannels must be 0: drainChannelPool closed 3 pool channels, each
	// calling CloseEphemeralChannel → Add(-1).
	assert.Equal(t, int32(0), conn.inUseChannels.Load(),
		"inUseChannels must be 0 after reconnect drains pool (P1 fix: drainChannelPool must decrement counter)")

	// Post-reconnect AcquireChannel must succeed (no fake-full).
	postCh, err := conn.AcquireChannel()
	require.NoError(t, err, "AcquireChannel must succeed after reconnect clears inUseChannels")
	conn.CloseEphemeralChannel(postCh)
}

// TestNewConnection_NegativeMaxChannelsPerConn_FallsBackToDefault verifies that
// MaxChannelsPerConn <= 0 values are replaced by defaultRMQMaxChannelsPerConn
// at setDefaults time. Production code must not be able to set a sentinel that
// disables the cap.
func TestNewConnection_NegativeMaxChannelsPerConn_FallsBackToDefault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input    int
		expected int
	}{
		{input: -1, expected: defaultRMQMaxChannelsPerConn},
		{input: -100, expected: defaultRMQMaxChannelsPerConn},
		{input: 0, expected: defaultRMQMaxChannelsPerConn},
		{input: 256, expected: 256},
		{input: 1024, expected: 1024},
	}

	for _, tc := range cases {
		cfg := Config{MaxChannelsPerConn: tc.input}
		cfg.setDefaults()
		assert.Equal(t, tc.expected, cfg.MaxChannelsPerConn,
			"MaxChannelsPerConn=%d must fall back to %d after setDefaults",
			tc.input, tc.expected)
	}
}

// TestSubscribeOnce_SetupFailure_ReleasesInUseChannel verifies that when
// subscribeOnce fails during setup (e.g. QueueDeclare returns an error),
// closeChannel is called which invokes CloseEphemeralChannel → inUseChannels
// is decremented.
//
// Before the P1 fix, closeChannel called ch.Close() directly, permanently
// leaking the inUseChannels slot and causing fake-full errors after enough
// failed subscription attempts.
func TestSubscribeOnce_SetupFailure_ReleasesInUseChannel(t *testing.T) {
	conn, mockConn := newTestConnectionWithCapAndMock(t, 1, 1<<20 /* uncapped */)

	ch := newMockChannel()
	// A permanent (non-AMQP) error so subscribeOnce returns immediately
	// without entering the reconnect loop.
	ch.queueDeclareErr = errors.New("rabbitmq: declare queue: permission denied")
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:   "setup-fail-queue",
		DLXExchange: "setup-fail.dlx",
		Clock:       clock.Real(),
	})

	// inUseChannels is 0 before subscribe.
	assert.Equal(t, int32(0), conn.inUseChannels.Load(), "inUseChannels must be 0 before subscribe")

	ctx := context.Background()
	// Subscribe returns error immediately (permanent setup failure).
	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "setup.fail.topic", CellID: "test-cell"}, entryToSubHandler(
		func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.Ack()
		},
	))
	require.Error(t, err, "Subscribe must return error on permanent setup failure")

	// inUseChannels must be back at 0 — closeChannel called CloseEphemeralChannel.
	assert.Equal(t, int32(0), conn.inUseChannels.Load(),
		"inUseChannels must be 0 after setup failure (P1 fix: closeChannel must call CloseEphemeralChannel)")
}

// TestSubscriptionRun_WaitAndClose_ReleasesInUseChannel verifies that
// subscriptionRun.waitAndClose (the normal subscription teardown path) calls
// conn.CloseEphemeralChannel, which decrements inUseChannels.
//
// Before the P1 fix, waitAndClose called ch.Close() directly, permanently
// leaking the inUseChannels slot on every subscription that completed normally.
func TestSubscriptionRun_WaitAndClose_ReleasesInUseChannel(t *testing.T) {
	conn, mockConn := newTestConnectionWithCapAndMock(t, 1, 1<<20 /* uncapped */)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:   "waitandclose-queue",
		DLXExchange: "waitandclose.dlx",
		Clock:       clock.Real(),
	})

	subCtx, subCancel := context.WithCancel(context.Background())
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "waitandclose.topic", CellID: "test-cell"}, entryToSubHandler(
			func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				return outbox.Ack()
			},
		))
	}()

	// Wait until subscriber has acquired its channel (inUseChannels == 1).
	require.Eventually(t, func() bool {
		return conn.inUseChannels.Load() == 1
	}, testtime.D2s, testtime.FastPoll, "subscriber must acquire channel before we cancel")

	// Cancel Subscribe ctx → consumeLoop exits.
	// Close deliveries channel first so consumeLoop sees closed channel and exits cleanly.
	close(ch.consumeDeliveries)
	subCancel()

	select {
	case err := <-subDone:
		assert.NoError(t, err, "Subscribe must return nil on clean cancel")
	case <-time.After(testtime.D3s):
		t.Fatal("Subscribe did not exit after ctx cancel")
	}

	// Call sub.Close() to ensure the Close sweep runs waitAndClose for any
	// runs that are still tracked (e.g. when waitAndClose ctx expired early).
	closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer closeCancel()
	require.NoError(t, sub.Close(closeCtx), "sub.Close must succeed")

	// inUseChannels must be 0 — waitAndClose (via subscribeOnce or Close sweep)
	// called CloseEphemeralChannel for the subscriber's channel.
	assert.Equal(t, int32(0), conn.inUseChannels.Load(),
		"inUseChannels must be 0 after subscription teardown (P1 fix: waitAndClose must call CloseEphemeralChannel)")
}
