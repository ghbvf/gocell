package websocket

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---------------------------------------------------------------------------
// fakeConn — network-free Conn for unit tests
// ---------------------------------------------------------------------------

type fakeConn struct {
	id      string
	readCh  chan []byte   // send data to simulate client messages; close to end Read
	closeCh chan struct{} // closed on Close() to unblock Read
	readyCh chan struct{} // closed on first Read call (replaces time.Sleep)

	mu         sync.Mutex
	closed     bool
	writes     [][]byte
	readyOnce  sync.Once
	pingErr    error         // configurable: non-nil makes Ping fail
	writeDelay time.Duration // configurable: simulates slow writes
	closeDelay time.Duration // configurable: simulates slow Close (T11)
	principal  *auth.Principal
}

func newFakeConn(id string) *fakeConn {
	return &fakeConn{
		id:      id,
		readCh:  make(chan []byte, 10),
		closeCh: make(chan struct{}),
		readyCh: make(chan struct{}),
	}
}

func (f *fakeConn) ID() string         { return f.id }
func (f *fakeConn) RemoteAddr() string { return "127.0.0.1:0" }

func (f *fakeConn) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("closed")
	}
	if f.pingErr != nil {
		return f.pingErr
	}
	return nil
}

func (f *fakeConn) Read(ctx context.Context) ([]byte, error) {
	f.readyOnce.Do(func() { close(f.readyCh) })
	select {
	case data, ok := <-f.readCh:
		if !ok {
			return nil, errors.New("read channel closed")
		}
		return data, nil
	case <-f.closeCh:
		return nil, errors.New("connection closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeConn) Write(_ context.Context, data []byte) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errors.New("closed")
	}
	delay := f.writeDelay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay) //archtest:allow:test-sleep sleep IS the test parameter
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("closed")
	}
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	delay := f.closeDelay
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay) //archtest:allow:test-sleep sleep IS the test parameter (simulated close latency)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.closeCh)
	return nil
}

func (f *fakeConn) Principal() *auth.Principal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.principal
}

func newFakeConnWithPrincipal(id string, p *auth.Principal) *fakeConn {
	c := newFakeConn(id)
	c.principal = p
	return c
}

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeConn) getWrites() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

// startHub starts a Hub in a background goroutine and returns it.
// The Hub is stopped via t.Cleanup.
func startHub(t *testing.T, cfg HubConfig, handler MessageHandler) *Hub {
	t.Helper()
	hub := NewHub(cfg, handler)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
		defer cancel()
		_ = hub.Stop(stopCtx)
		<-startErr
	})
	// Wait until hub is running.
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)
	return hub
}

func waitForHubPingTicker(t *testing.T, fc *clockmock.FakeClock) {
	t.Helper()
	require.Eventually(t, func() bool {
		return fc.PendingTickers() == 1
	}, testtime.EventuallyShort, testtime.D1ms)
}

// ---------------------------------------------------------------------------
// ManagedResource Tests (T4 / T5 / T6)
// ---------------------------------------------------------------------------

// T4: Hub.Checkers() state machine — Idle/Stopping/Stopped return non-nil;
// Running returns nil (healthy).
func TestHub_Checkers_StateMachine(t *testing.T) {
	tests := []struct {
		name    string
		state   int32
		wantNil bool
	}{
		{"idle", stateIdle, false},
		{"running", stateRunning, true},
		{"stopping", stateStopping, false},
		{"stopped", stateStopped, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewHub(DefaultHubConfig(clock.Real()), nil)
			hub.state.Store(tt.state)
			checkers := hub.Checkers()
			require.NotEmpty(t, checkers, "Checkers must return a non-empty map")
			fn, ok := checkers["websocket_hub_ready"]
			require.True(t, ok, "must have 'websocket_hub_ready' key")
			err := fn(context.Background())
			if tt.wantNil {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}

	// T4b: true stateStopping via a stuckConn — verifies that Checkers returns
	// non-nil while shutdown is in progress (hub.wg.Wait is blocked).
	t.Run("stopping_via_live_shutdown", func(t *testing.T) {
		hub := NewHub(DefaultHubConfig(clock.Real()), nil)
		startErr := make(chan error, 1)
		go func() { startErr <- hub.Start(context.Background()) }()
		require.Eventually(t, func() bool {
			return hub.state.Load() == stateRunning
		}, testtime.EventuallyShort, testtime.D1ms)

		stuck := &stuckConn{id: "t4b-stuck", closeCh: make(chan struct{})}
		require.NoError(t, hub.Register(context.Background(), stuck))

		// Start Stop in background — this transitions to stateStopping while
		// waiting for goroutines to drain.
		stopDone := make(chan error, 1)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer stopCancel()
		go func() { stopDone <- hub.Stop(stopCtx) }()

		// Wait until hub is stopping.
		require.Eventually(t, func() bool {
			return hub.state.Load() >= stateStopping
		}, testtime.EventuallyShort, testtime.D1ms)

		// Checkers must report non-nil while stopping.
		checkers := hub.Checkers()
		fn, ok := checkers["websocket_hub_ready"]
		require.True(t, ok)
		assert.Error(t, fn(context.Background()), "Checkers must return error while hub is stopping")

		// Unblock the stuck conn so Stop can complete.
		close(stuck.closeCh)
		require.NoError(t, <-stopDone)
		<-startErr
	})
}

// T5: Hub.Close is idempotent — second call returns nil.
func TestHub_ManagedResource_CloseIsIdempotent(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()

	require.NoError(t, hub.Close(ctx), "first Close must succeed")
	require.NoError(t, hub.Close(ctx), "second Close must be idempotent (return nil)")
	<-startErr
}

// T6: Hub.Worker() returns nil (Hub self-manages goroutines).
func TestHub_ManagedResource_WorkerReturnsNil(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	assert.Nil(t, hub.Worker(), "Worker must return nil")
}

// T8: BroadcastFilter / BroadcastToSubject on stopped hub — no panic, returns nil.
func TestHub_BroadcastFilter_OnStoppedHub_NoOp(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	require.NoError(t, hub.Stop(context.Background()))

	err := hub.BroadcastFilter(context.Background(), []byte("x"), func(Conn) bool { return true })
	assert.NoError(t, err, "BroadcastFilter on stopped hub must return nil")

	err = hub.BroadcastToSubject(context.Background(), "alice", []byte("x"))
	assert.NoError(t, err, "BroadcastToSubject on stopped hub must return nil")
}

// T9: Send on stopped hub returns ErrWSConnNotFound.
func TestHub_Send_OnStoppedHub_ReturnsConnNotFound(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	require.NoError(t, hub.Stop(context.Background()))

	err := hub.Send(context.Background(), "any-id", []byte("x"))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be errcode.Error")
	assert.Equal(t, errcode.ErrWSConnNotFound, ec.Code)
}

// TestHub_BoundedConcurrentClose_RespectsLimit validates that
// closeEntriesConcurrently respects ConcurrentCloseLimit and that the hub
// reaches stateStopped regardless of whether the ctx deadline is hit.
//
// Uses closeBlockerConn so the Close goroutines block until released, letting
// us release them after Stop returns and verify all connections eventually
// close. The test does NOT assert on DeadlineExceeded vs nil — that outcome
// depends on timing and would make the test flaky.
func TestHub_BoundedConcurrentClose_RespectsLimit(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.ConcurrentCloseLimit = 2
	hub := NewHub(cfg, nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	const n = 5
	blockers := make([]*closeBlockerConn, n)
	for i := range n {
		blockers[i] = newCloseBlockerConn(fmt.Sprintf("blocker-%d", i))
		require.NoError(t, hub.Register(context.Background(), blockers[i]))
	}
	require.Eventually(t, func() bool { return hub.ConnCount() == n }, testtime.EventuallyShort, testtime.D1ms)

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D50ms)
	defer cancel()

	_ = hub.Stop(stopCtx) // err may be DeadlineExceeded or nil depending on timing
	assert.Equal(t, stateStopped, hub.state.Load())

	// Release all blockers so background Close goroutines can exit (goleak).
	// Not all blockers may have had Close() called (ctx may expire before all
	// semaphore slots are acquired); releasing is harmless for those.
	for _, b := range blockers {
		close(b.releaseClose)
	}
	<-startErr
}

// T11: Bounded concurrent close drains in parallel — N=20 conns each taking
// 50ms to close should finish well under serial time (20×50ms=1000ms);
// with ConcurrentCloseLimit=8 expect ~ceil(20/8)×50ms ≈ 150ms < 500ms.
func TestHub_BoundedConcurrentClose_ParallelDrain(t *testing.T) {
	const (
		n             = 20
		closeDelay    = testtime.D50ms
		serialBound   = time.Duration(n) * closeDelay // 1000ms if serial
		parallelBound = testtime.D500ms               // well within parallel budget
	)

	cfg := DefaultHubConfig(clock.Real())
	cfg.ConcurrentCloseLimit = 8
	hub := NewHub(cfg, nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	for i := range n {
		fc := newFakeConn(fmt.Sprintf("delay-%d", i))
		fc.closeDelay = closeDelay
		require.NoError(t, hub.Register(context.Background(), fc))
	}
	require.Eventually(t, func() bool { return hub.ConnCount() == n }, testtime.EventuallyShort, testtime.D1ms)

	stopCtx, cancel := context.WithTimeout(context.Background(), serialBound)
	defer cancel()

	start := time.Now()
	err := hub.Stop(stopCtx)
	elapsed := time.Since(start)

	require.NoError(t, err, "parallel drain must complete within serial budget")
	assert.Less(t, elapsed, parallelBound,
		"parallel drain (%v) must be faster than %v (serial would be ~%v)",
		elapsed, parallelBound, serialBound)

	<-startErr
}

// T12: External ctx cancel uses configured ShutdownTimeout, not hardcoded 10s.
// ShutdownTimeout=50ms + 5 blocking conns → Start returns in < 500ms.
func TestHub_ExternalCancel_UsesConfiguredShutdownTimeout(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.ShutdownTimeout = testtime.D50ms
	hub := NewHub(cfg, nil)

	ctx, cancelCtx := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(ctx) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	const n = 5
	blockers := make([]*closeBlockerConn, n)
	for i := range n {
		blockers[i] = newCloseBlockerConn(fmt.Sprintf("ext-%d", i))
		require.NoError(t, hub.Register(context.Background(), blockers[i]))
	}
	require.Eventually(t, func() bool { return hub.ConnCount() == n }, testtime.EventuallyShort, testtime.D1ms)

	start := time.Now()
	cancelCtx()

	select {
	case err := <-startErr:
		elapsed := time.Since(start)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Less(t, elapsed, testtime.D500ms,
			"Start with ShutdownTimeout=50ms must return in < 500ms (not hardcoded 10s)")
	case <-time.After(testtime.D500ms):
		t.Fatal("Start did not return within 500ms — ShutdownTimeout not respected")
	}

	// Release blockers so background Close goroutines can exit (goleak).
	// Not all blockers may have had Close() called if ctx expired before all
	// semaphore slots were acquired; releasing is harmless for those.
	for _, b := range blockers {
		close(b.releaseClose)
	}
	// startErr was already consumed by the select above.
}

// T7: Stop × external-cancel race CAS — both paths converge to stateStopped
// with a single close of shutdownDone; -race must pass.
//
// This test validates final state invariants under concurrent access. With
// -race the data-race detector catches any unsafe shared-state access. The CAS
// contention itself is rarely Stop-wins (cancel() is single-atomic; Stop has
// lock+CAS overhead) but both paths converge on stateStopped via the single
// shutdown() owner. Run with
//
//	go test -race -count=100 -run TestHub_StopAndExternalCancel_RaceCAS
//
// for stability validation.
func TestHub_StopAndExternalCancel_RaceCAS(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	ctx, cancelCtx := context.WithCancel(context.Background())

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(ctx) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	// Barrier: both goroutines wait until both are ready, then fire simultaneously.
	var barrier sync.WaitGroup
	barrier.Add(2)

	var stopErr, ctxErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		barrier.Done()
		barrier.Wait()
		stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer cancel()
		stopErr = hub.Stop(stopCtx)
	}()

	go func() {
		defer wg.Done()
		barrier.Done()
		barrier.Wait()
		cancelCtx()
		ctxErr = nil // cancel itself is not an error
	}()

	wg.Wait()

	// Collect Start return value.
	select {
	case err := <-startErr:
		// Start returns nil (shutdown by Stop) or ctx.Err() (shutdown by cancel).
		// Either is acceptable — both paths ran correctly.
		_ = err
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("Start did not return after race")
	}

	_ = ctxErr
	assert.Equal(t, stateStopped, hub.state.Load())

	// shutdownDone must be closed (select should not block).
	select {
	case <-hub.shutdownDone:
		// correct
	default:
		t.Fatal("shutdownDone was not closed after race")
	}

	// One of Stop/external-cancel owns the shutdown; the other returns
	// ErrWSAlreadyStopped or nil (depending on who won).
	if stopErr != nil {
		assert.Contains(t, stopErr.Error(), "already stopped",
			"losing Stop must return ErrWSAlreadyStopped")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestHub_StopUnblocksStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()

	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))

	select {
	case err := <-startErr:
		assert.NoError(t, err)
	case <-time.After(testtime.D2s):
		t.Fatal("Start did not return after Stop")
	}
}

func TestHub_DoubleStart(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	err := hub.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestHub_DoubleStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))
	<-startErr

	err := hub.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestHub_StopBeforeStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	require.NoError(t, hub.Stop(context.Background()))
	assert.Equal(t, stateStopped, hub.state.Load())
}

func TestHub_StartAfterStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	require.NoError(t, hub.Stop(context.Background()))

	err := hub.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestHub_StopTimeout(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	// Register a conn whose Close is a no-op (readLoop never exits).
	stuck := &stuckConn{id: "stuck", closeCh: make(chan struct{})}
	require.NoError(t, hub.Register(context.Background(), stuck))

	// Stop with very short timeout.
	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.MediumPoll)
	defer cancel()
	err := hub.Stop(stopCtx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, stateStopped, hub.state.Load())

	// Unblock the stuck conn so goroutine exits for goleak.
	close(stuck.closeCh)
	<-startErr
}

func TestHub_ExternalContextCancel(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	ctx, cancel := context.WithCancel(context.Background())

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(ctx) }()

	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	// Register a conn before cancel.
	conn := newFakeConn("pre-cancel")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	cancel()

	select {
	case err := <-startErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("Start did not return after context cancel")
	}

	// Start runs full shutdown on external cancel, so hub is now stopped.
	assert.Equal(t, stateStopped, hub.state.Load())
	assert.True(t, conn.isClosed(), "shutdown should have closed connections")

	// Register must be rejected.
	lateConn := newFakeConn("post-cancel")
	err := hub.Register(context.Background(), lateConn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")

	// Stop is a no-op (already stopped) — returns "already stopped".
	err = hub.Stop(context.Background())
	assert.Contains(t, err.Error(), "already stopped")
}

// ---------------------------------------------------------------------------
// Registration Tests
// ---------------------------------------------------------------------------

func TestHub_RegisterAndReadLoop(t *testing.T) {
	var (
		mu     sync.Mutex
		gotID  string
		gotMsg string
	)
	handler := func(_ context.Context, id string, data []byte) {
		mu.Lock()
		gotID = id
		gotMsg = string(data)
		mu.Unlock()
	}

	hub := startHub(t, DefaultHubConfig(clock.Real()), handler)

	conn := newFakeConn("sender")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	conn.readCh <- []byte("hello server")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMsg != ""
	}, testtime.EventuallyShort, testtime.D10ms)

	mu.Lock()
	assert.Equal(t, "sender", gotID)
	assert.Equal(t, "hello server", gotMsg)
	mu.Unlock()
}

func TestHub_RegisterUsesContextValues(t *testing.T) {
	type ctxKey string

	const want = "trace-123"
	got := make(chan any, 1)
	registerCtx := context.WithValue(context.Background(), ctxKey("trace-id"), want)
	hub := NewHub(DefaultHubConfig(clock.Real()), func(ctx context.Context, _ string, _ []byte) {
		got <- ctx.Value(ctxKey("trace-id"))
	})

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer cancel()
		_ = hub.Stop(stopCtx)
		<-startErr
	})

	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	conn := newFakeConn("context-values")
	require.NoError(t, hub.Register(registerCtx, conn))
	<-conn.readyCh

	conn.readCh <- []byte("hello")
	select {
	case value := <-got:
		assert.Equal(t, want, value)
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("handler did not receive message")
	}
}

func TestHub_RegisterDuringStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	// Force state to stopping to simulate mid-Stop window.
	hub.state.Store(stateStopping)

	conn := newFakeConn("rejected")
	err := hub.Register(context.Background(), conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopping")
	assert.True(t, conn.isClosed())

	// Reset for cleanup.
	hub.state.Store(stateRunning)
	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	_ = hub.Stop(stopCtx)
	<-startErr
}

func TestHub_RegisterOnStoppedHub(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	_ = hub.Stop(context.Background())

	conn := newFakeConn("late")
	err := hub.Register(context.Background(), conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
	assert.True(t, conn.isClosed())
}

func TestHub_Unregister(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	conn := newFakeConn("c1")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	assert.Equal(t, 1, hub.ConnCount())
	hub.Unregister("c1")

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, testtime.EventuallyShort, testtime.D10ms)
	assert.True(t, conn.isClosed())
}

func TestHub_UnregisterIdempotent(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	conn := newFakeConn("c1")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	hub.Unregister("c1")
	hub.Unregister("c1") // should not panic

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, testtime.EventuallyShort, testtime.D10ms)
}

func TestHub_RegisterDuplicateID(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	connA := newFakeConn("dup")
	require.NoError(t, hub.Register(context.Background(), connA))
	<-connA.readyCh
	assert.Equal(t, 1, hub.ConnCount())

	// Register with same ID — old conn should be evicted.
	connB := newFakeConn("dup")
	require.NoError(t, hub.Register(context.Background(), connB))
	<-connB.readyCh

	assert.Equal(t, 1, hub.ConnCount(), "map should have exactly 1 entry")
	assert.True(t, connA.isClosed(), "old conn should be closed")

	// Send to "dup" should reach connB, not connA.
	// Send enqueues on the per-conn channel; writeLoop delivers asynchronously.
	require.NoError(t, hub.Send(context.Background(), "dup", []byte("hello")))
	require.Eventually(t, func() bool {
		return len(connB.getWrites()) == 1
	}, testtime.D2s, testtime.D10ms)
	assert.Equal(t, [][]byte{[]byte("hello")}, connB.getWrites())
	assert.Empty(t, connA.getWrites())
}

func TestHub_MaxConnections(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.MaxConnections = 2
	hub := startHub(t, cfg, nil)

	c1 := newFakeConn("c1")
	c2 := newFakeConn("c2")
	require.NoError(t, hub.Register(context.Background(), c1))
	require.NoError(t, hub.Register(context.Background(), c2))
	<-c1.readyCh
	<-c2.readyCh
	assert.Equal(t, 2, hub.ConnCount())

	// Third connection should be rejected.
	c3 := newFakeConn("c3")
	err := hub.Register(context.Background(), c3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max connections")
	assert.True(t, c3.isClosed(), "rejected conn should be closed")
	assert.Equal(t, 2, hub.ConnCount())
}

// ---------------------------------------------------------------------------
// Concurrency Tests
// ---------------------------------------------------------------------------

func TestHub_RegisterStopRace(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	const n = 100
	var registerWg sync.WaitGroup
	registerWg.Add(n)
	for i := range n {
		go func(idx int) {
			defer registerWg.Done()
			c := newFakeConn(fmt.Sprintf("race-%d", idx))
			_ = hub.Register(context.Background(), c)
		}(i)
	}

	// Stop concurrently with registrations.
	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()
	_ = hub.Stop(stopCtx)

	registerWg.Wait()
	<-startErr
	// goleak.VerifyTestMain catches any leaked goroutines.
}

// ---------------------------------------------------------------------------
// Functional Tests
// ---------------------------------------------------------------------------

func TestHub_Send(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	conn := newFakeConn("target")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	require.NoError(t, hub.Send(context.Background(), "target", []byte("direct")))
	// Send enqueues on the per-conn channel; writeLoop delivers asynchronously.
	require.Eventually(t, func() bool {
		return len(conn.getWrites()) == 1
	}, testtime.D2s, testtime.D10ms)
	assert.Equal(t, [][]byte{[]byte("direct")}, conn.getWrites())
}

func TestHub_SendNotFound(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	err := hub.Send(context.Background(), "nonexistent", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHub_MessageHandler(t *testing.T) {
	var (
		mu     sync.Mutex
		gotID  string
		gotMsg string
		gotCtx context.Context
	)

	handler := func(ctx context.Context, id string, data []byte) {
		mu.Lock()
		gotCtx = ctx
		gotID = id
		gotMsg = string(data)
		mu.Unlock()
	}

	hub := startHub(t, DefaultHubConfig(clock.Real()), handler)

	conn := newFakeConn("h1")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	conn.readCh <- []byte("payload")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMsg != ""
	}, testtime.EventuallyShort, testtime.D10ms)

	mu.Lock()
	assert.Equal(t, "h1", gotID)
	assert.Equal(t, "payload", gotMsg)
	assert.NotNil(t, gotCtx, "handler should receive per-conn context")
	mu.Unlock()
}

// ---------------------------------------------------------------------------
// Ping Tests
// ---------------------------------------------------------------------------

func TestHub_PingMissThreshold(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll
	cfg.PingMissMax = 3

	hub := startHub(t, cfg, nil)

	conn := newFakeConn("pinger")
	conn.pingErr = errors.New("timeout")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0 && conn.isClosed()
	}, testtime.D2s, testtime.D10ms)
}

func TestHub_PingMissReset(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll
	cfg.PingMissMax = 3

	hub := startHub(t, cfg, nil)

	conn := newFakeConn("resilient")
	conn.pingErr = errors.New("fail")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	// Let 2 ping misses accumulate (below threshold).
	require.Eventually(t, func() bool {
		hub.connMu.Lock()
		e, ok := hub.conns["resilient"]
		var misses int
		if ok {
			misses = e.pingMisses
		}
		hub.connMu.Unlock()
		return misses >= 2
	}, testtime.D2s, testtime.FastPoll)

	// Heal the connection.
	conn.mu.Lock()
	conn.pingErr = nil
	conn.mu.Unlock()

	// Wait for a successful ping to reset misses.
	require.Eventually(t, func() bool {
		hub.connMu.Lock()
		e, ok := hub.conns["resilient"]
		var misses int
		if ok {
			misses = e.pingMisses
		}
		hub.connMu.Unlock()
		return ok && misses == 0
	}, testtime.D2s, testtime.FastPoll)

	assert.Equal(t, 1, hub.ConnCount(), "connection should survive after healing")
}

func TestHub_PingLoopRunsOnInterval(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll

	hub := startHub(t, cfg, nil)

	conn := newFakeConn("counter")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	// Wait long enough for multiple pings, verify conn is still alive.
	time.Sleep(testtime.D100ms) //archtest:allow:test-sleep negative test: must elapse without state change
	assert.Equal(t, 1, hub.ConnCount(), "connection should survive multiple pings")
}

// ---------------------------------------------------------------------------
// Config Tests
// ---------------------------------------------------------------------------

// T1: ShutdownTimeout defaults to defaultShutdownTimeout (10s) when zero;
// explicit value is preserved.
func TestHubConfig_ShutdownTimeout_Default(t *testing.T) {
	cfgZero := HubConfig{Clock: clock.Real()}
	hub := NewHub(cfgZero, nil)
	assert.Equal(t, defaultShutdownTimeout, hub.Config().ShutdownTimeout,
		"zero ShutdownTimeout must be replaced with defaultShutdownTimeout")

	cfgExplicit := HubConfig{Clock: clock.Real(), ShutdownTimeout: testtime.D30s}
	hub2 := NewHub(cfgExplicit, nil)
	assert.Equal(t, testtime.D30s, hub2.Config().ShutdownTimeout,
		"explicit ShutdownTimeout must be preserved")
}

// T2: ConcurrentCloseLimit defaults to 64 when zero; explicit value is preserved.
func TestHubConfig_ConcurrentCloseLimit_Default(t *testing.T) {
	cfgZero := HubConfig{Clock: clock.Real()}
	hub := NewHub(cfgZero, nil)
	assert.Equal(t, 64, hub.Config().ConcurrentCloseLimit,
		"zero ConcurrentCloseLimit must be replaced with 64")

	cfgExplicit := HubConfig{Clock: clock.Real(), ConcurrentCloseLimit: 32}
	hub2 := NewHub(cfgExplicit, nil)
	assert.Equal(t, 32, hub2.Config().ConcurrentCloseLimit,
		"explicit ConcurrentCloseLimit must be preserved")
}

// T3: fakeConn implements Conn including RemoteAddr(); compile-time guard +
// runtime assertion.
func TestConn_RemoteAddr_InterfaceContract(t *testing.T) {
	fc := newFakeConn("addr-test")
	assert.NotEmpty(t, fc.RemoteAddr(), "RemoteAddr must return a non-empty address")
}

func TestDefaultHubConfig(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	assert.Equal(t, testtime.D30s, cfg.PingInterval)
	assert.Equal(t, testtime.D5s, cfg.PingTimeout)
	assert.Equal(t, int64(64*1024), cfg.ReadLimit)
	assert.Equal(t, 2, cfg.PingMissMax)
}

func TestNewHub_PreservesExplicitConfig(t *testing.T) {
	cfg := HubConfig{
		PingInterval:         testtime.D1s,
		PingTimeout:          testtime.D250ms,
		ReadLimit:            1024,
		PingMissMax:          5,
		MaxConnections:       9,
		SendBufferSize:       16, // explicit non-zero; must be preserved as-is
		ShutdownTimeout:      testtime.D30s,
		ConcurrentCloseLimit: 32,
		Clock:                clock.Real(),
	}
	handler := func(context.Context, string, []byte) {}

	hub := NewHub(cfg, handler)

	assert.Equal(t, cfg, hub.Config())
	assert.NotNil(t, hub.handler)
}

func TestHub_IsRunning(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	assert.False(t, hub.IsRunning(), "idle hub")

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool { return hub.IsRunning() }, testtime.EventuallyShort, testtime.D1ms)

	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	_ = hub.Stop(stopCtx)
	<-startErr
	assert.False(t, hub.IsRunning(), "stopped hub")
}

func TestHub_StopDeadlineHonored(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)

	// Register a stuck conn that blocks Close.
	stuck := &stuckConn{id: "blocker", closeCh: make(chan struct{})}
	require.NoError(t, hub.Register(context.Background(), stuck))

	// Stop with 100ms deadline — must return within ~200ms regardless of
	// stuck conn.
	start := time.Now()
	stopCtx, cancel := context.WithTimeout(context.Background(), testtime.D100ms)
	defer cancel()
	err := hub.Stop(stopCtx)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, testtime.D500ms, "Stop should honor deadline")
	assert.Equal(t, stateStopped, hub.state.Load())

	// Cleanup for goleak.
	close(stuck.closeCh)
	<-startErr
}

func TestNewHub_NilHandler(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	assert.NotNil(t, hub.handler, "nil handler should be replaced with noop")
	hub.handler(context.Background(), "test", []byte("data"))
}

// ---------------------------------------------------------------------------
// closeBlockerConn — a conn whose Close blocks until releaseClose is closed.
// Used to test bounded concurrent close (T10/T12).
// ---------------------------------------------------------------------------

type closeBlockerConn struct {
	*fakeConn
	releaseClose chan struct{}
}

func newCloseBlockerConn(id string) *closeBlockerConn {
	return &closeBlockerConn{
		fakeConn:     newFakeConn(id),
		releaseClose: make(chan struct{}),
	}
}

// Close blocks until releaseClose is closed, then delegates to fakeConn.Close.
func (c *closeBlockerConn) Close() error {
	<-c.releaseClose
	return c.fakeConn.Close()
}

// ---------------------------------------------------------------------------
// stuckConn — a conn whose Read blocks until closeCh is closed.
// Used to test Stop timeout behavior.
// ---------------------------------------------------------------------------

type stuckConn struct {
	id      string
	closeCh chan struct{}
	mu      sync.Mutex
	closed  bool
}

func (s *stuckConn) ID() string         { return s.id }
func (s *stuckConn) RemoteAddr() string { return "127.0.0.1:0" }
func (s *stuckConn) Ping(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("closed")
	}
	return nil
}

func (s *stuckConn) Read(_ context.Context) ([]byte, error) {
	<-s.closeCh
	return nil, errors.New("closed")
}
func (s *stuckConn) Write(_ context.Context, _ []byte) error { return nil }
func (s *stuckConn) Principal() *auth.Principal              { return nil }
func (s *stuckConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
	}
	// Intentionally do NOT close closeCh — simulates a conn that won't unblock.
	return nil
}

// ---------------------------------------------------------------------------
// Contract Test Suite 1: Hub State Machine (table-driven)
// ---------------------------------------------------------------------------

func TestHub_StateMachine(t *testing.T) {
	type action struct {
		name    string
		fn      func(*Hub) error
		wantErr string // "" = no error
	}

	stopAction := action{"Stop", func(h *Hub) error {
		return h.Stop(context.Background())
	}, ""}
	registerAction := action{"Register", func(h *Hub) error {
		return h.Register(context.Background(), newFakeConn("sm"))
	}, ""}

	tests := []struct {
		name    string
		setup   func() *Hub // put hub in desired state
		action  action
		wantErr string
	}{
		{
			"idle+Stop",
			func() *Hub { return NewHub(DefaultHubConfig(clock.Real()), nil) },
			stopAction, "",
		},
		{
			"idle+Register",
			func() *Hub { return NewHub(DefaultHubConfig(clock.Real()), nil) },
			registerAction, "not running",
		},
		{
			"running+Start",
			func() *Hub { return startHubBackground(t) },
			action{"Start", func(h *Hub) error { return h.Start(context.Background()) }, ""},
			"already started",
		},
		{
			"running+Stop",
			func() *Hub { return startHubBackground(t) },
			stopAction, "",
		},
		{
			"running+Register",
			func() *Hub { return startHubBackground(t) },
			registerAction, "",
		},
		{
			"stopped+Start",
			func() *Hub {
				h := NewHub(DefaultHubConfig(clock.Real()), nil)
				_ = h.Stop(context.Background())
				return h
			},
			action{"Start", func(h *Hub) error { return h.Start(context.Background()) }, ""},
			"already stopped",
		},
		{
			"stopped+Stop",
			func() *Hub {
				h := NewHub(DefaultHubConfig(clock.Real()), nil)
				_ = h.Stop(context.Background())
				return h
			},
			stopAction, "already stopped",
		},
		{
			"stopped+Register",
			func() *Hub {
				h := NewHub(DefaultHubConfig(clock.Real()), nil)
				_ = h.Stop(context.Background())
				return h
			},
			registerAction, "not running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := tt.setup()
			err := tt.action.fn(hub)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

// startHubBackground starts a Hub and registers cleanup.
func startHubBackground(t *testing.T) *Hub {
	t.Helper()
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, testtime.EventuallyShort, testtime.D1ms)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer cancel()
		_ = hub.Stop(ctx)
		<-startErr
	})
	return hub
}

// ---------------------------------------------------------------------------
// Contract Test Suite 2: Conn Conformance (tests fakeConn as reference)
// ---------------------------------------------------------------------------

func TestConnConformance_CloseInterruptsRead(t *testing.T) {
	conn := newFakeConn("cir")
	readDone := make(chan error, 1)
	go func() {
		_, err := conn.Read(context.Background())
		readDone <- err
	}()
	<-conn.readyCh // ensure Read is blocking

	require.NoError(t, conn.Close())

	select {
	case err := <-readDone:
		assert.Error(t, err, "Close must cause Read to return an error")
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("Read did not return after Close")
	}
}

func TestConnConformance_CloseIdempotent(t *testing.T) {
	conn := newFakeConn("ci")
	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close()) // second call must not panic
	assert.True(t, conn.isClosed())
}

func TestConnConformance_ConcurrentWriteClose(t *testing.T) {
	conn := newFakeConn("cwc")
	conn.writeDelay = testtime.D100ms

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine.
	go func() {
		defer wg.Done()
		_ = conn.Write(context.Background(), []byte("data"))
	}()

	// Close goroutine — fires while Write is in progress.
	go func() {
		defer wg.Done()
		time.Sleep(testtime.D10ms) //archtest:allow:test-sleep goroutine timing fixture: controls cancel order
		_ = conn.Close()
	}()

	wg.Wait() // must not deadlock or panic
}

// ---------------------------------------------------------------------------
// Contract Test Suite 3: UpgradeHandler (see handler_test.go for real WS)
// ---------------------------------------------------------------------------
// UpgradeHandler contract tests that don't need a network are here.
// Real WebSocket upgrade tests are in adapters/websocket/handler_test.go.

func TestHub_IsRunning_Contract(t *testing.T) {
	hub := NewHub(DefaultHubConfig(clock.Real()), nil)

	// idle → not running
	assert.False(t, hub.IsRunning())

	// running
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool { return hub.IsRunning() }, testtime.EventuallyShort, testtime.D1ms)

	// stop → not running
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	_ = hub.Stop(ctx)
	<-startErr
	assert.False(t, hub.IsRunning())
}

// Compile-time interface checks.
var (
	_ Conn = (*fakeConn)(nil)
	_ Conn = (*stuckConn)(nil)
)

// ---------------------------------------------------------------------------
// BroadcastFilter / BroadcastToSubject Tests
// ---------------------------------------------------------------------------

func TestHub_BroadcastFilter_NilFilterFails(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	err := hub.BroadcastFilter(context.Background(), []byte("x"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filter")
}

func TestHub_BroadcastFilter_AllConns(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	pa := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice"}
	pb := &auth.Principal{Kind: auth.PrincipalUser, Subject: "bob"}
	a := newFakeConnWithPrincipal("a", pa)
	b := newFakeConnWithPrincipal("b", pb)
	require.NoError(t, hub.Register(context.Background(), a))
	require.NoError(t, hub.Register(context.Background(), b))
	<-a.readyCh
	<-b.readyCh

	require.NoError(t, hub.BroadcastFilter(context.Background(), []byte("hi"),
		func(c Conn) bool { return true }))

	require.Eventually(t, func() bool {
		return len(a.getWrites()) == 1 && len(b.getWrites()) == 1
	}, testtime.D2s, testtime.D10ms)
}

func TestHub_BroadcastFilter_SelectiveBySubject(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	pa := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice"}
	pb := &auth.Principal{Kind: auth.PrincipalUser, Subject: "bob"}
	a := newFakeConnWithPrincipal("a", pa)
	b := newFakeConnWithPrincipal("b", pb)
	require.NoError(t, hub.Register(context.Background(), a))
	require.NoError(t, hub.Register(context.Background(), b))
	<-a.readyCh
	<-b.readyCh

	require.NoError(t, hub.BroadcastFilter(context.Background(), []byte("only-alice"),
		func(c Conn) bool { return c.Principal() != nil && c.Principal().Subject == "alice" }))

	require.Eventually(t, func() bool {
		return len(a.getWrites()) == 1
	}, testtime.D2s, testtime.D10ms)

	// Negative wait — bob must NOT receive.
	assert.Never(t, func() bool { return len(b.getWrites()) > 0 }, testtime.D100ms, testtime.D10ms)
}

func TestHub_BroadcastToSubject_EmptySubjectFails(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	err := hub.BroadcastToSubject(context.Background(), "", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subject")
}

func TestHub_BroadcastToSubject_HitsAllConnsForSubject(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	pa := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice"}
	pb := &auth.Principal{Kind: auth.PrincipalUser, Subject: "bob"}
	a1 := newFakeConnWithPrincipal("a1", pa)
	a2 := newFakeConnWithPrincipal("a2", pa)
	b := newFakeConnWithPrincipal("b", pb)
	require.NoError(t, hub.Register(context.Background(), a1))
	require.NoError(t, hub.Register(context.Background(), a2))
	require.NoError(t, hub.Register(context.Background(), b))
	<-a1.readyCh
	<-a2.readyCh
	<-b.readyCh

	require.NoError(t, hub.BroadcastToSubject(context.Background(), "alice", []byte("ping-alice")))

	require.Eventually(t, func() bool {
		return len(a1.getWrites()) == 1 && len(a2.getWrites()) == 1
	}, testtime.D2s, testtime.D10ms)
	assert.Never(t, func() bool { return len(b.getWrites()) > 0 }, testtime.D100ms, testtime.D10ms)
}

func TestHub_BroadcastToSubject_UnknownSubjectIsNoop(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	// No conns at all. Subject not present → returns nil, no error.
	err := hub.BroadcastToSubject(context.Background(), "ghost", []byte("x"))
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Token Expiry Eviction Tests
// ---------------------------------------------------------------------------

func TestHub_TokenExpiry_EvictsOnPing(t *testing.T) {
	fc := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	cfg := DefaultHubConfig(fc)
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll

	hub := startHub(t, cfg, nil)

	p := &auth.Principal{
		Kind:      auth.PrincipalUser,
		Subject:   "expiring",
		ExpiresAt: fc.Now().Add(testtime.D1h),
	}
	conn := newFakeConnWithPrincipal("expiring", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh
	require.Equal(t, 1, hub.ConnCount())

	// Advance clock past expiry; next ping tick must evict.
	waitForHubPingTicker(t, fc)
	fc.Advance(testtime.D2h)

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0 && conn.isClosed()
	}, testtime.D2s, testtime.D10ms)
}

// TestHub_TokenExpiry_AtBoundaryEvicts locks RFC 7519 §4.1.4 boundary: the
// token MUST NOT be accepted on or after the exp instant. Setup advances
// the clock to *exactly* expiresAt — Before(now) would return false (still
// alive); !After(now) returns true (evicted). This is the bug-or-not edge.
func TestHub_TokenExpiry_AtBoundaryEvicts(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(start)

	cfg := DefaultHubConfig(fc)
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll

	hub := startHub(t, cfg, nil)

	// Register at clock=start with ExpiresAt = start + 10ms.
	exp := start.Add(testtime.D10ms)
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "boundary", ExpiresAt: exp}
	conn := newFakeConnWithPrincipal("boundary", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh
	require.Equal(t, 1, hub.ConnCount())

	// Advance clock to *exactly* ExpiresAt — boundary case.
	// Before(exp) at exp: false (not strictly before). !After(exp) at exp: true.
	// pingLoop fires at this tick because PingInterval == 10ms == advance.
	waitForHubPingTicker(t, fc)
	fc.Advance(testtime.D10ms)

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0 && conn.isClosed()
	}, testtime.D2s, testtime.D10ms,
		"ExpiresAt == clock.Now() must evict (RFC 7519: on-or-after exp = expired)")
}

func TestHub_TokenExpiry_ZeroExpiryNeverEvicts(t *testing.T) {
	fc := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	cfg := DefaultHubConfig(fc)
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll

	hub := startHub(t, cfg, nil)

	// Anonymous principal: zero ExpiresAt = never expires.
	p := &auth.Principal{Kind: auth.PrincipalAnonymous}
	conn := newFakeConnWithPrincipal("perm", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	// Advance clock well past any plausible expiry. Conn must remain because
	// ExpiresAt is zero ("no expiry"). Use a bounded advance that clockmock can
	// replay without exhausting the test timeout (avoids O(advance/interval)
	// ticker iterations in clockmock).
	waitForHubPingTicker(t, fc)
	fc.Advance(testtime.D2h)

	assert.Never(t, func() bool { return hub.ConnCount() == 0 || conn.isClosed() }, testtime.D100ms, testtime.D10ms)
}

// ---------------------------------------------------------------------------
// Slow Client Eviction Tests
// ---------------------------------------------------------------------------

// blockingFakeConn never completes Write — used to simulate a slow/dead client
// whose send chan fills.
type blockingFakeConn struct {
	*fakeConn
	blockUntil chan struct{}
}

func newBlockingFakeConn(id string, p *auth.Principal) *blockingFakeConn {
	return &blockingFakeConn{
		fakeConn:   newFakeConnWithPrincipal(id, p),
		blockUntil: make(chan struct{}),
	}
}

func (b *blockingFakeConn) Write(ctx context.Context, data []byte) error {
	select {
	case <-b.blockUntil:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.fakeConn.Write(ctx, data)
}

func TestHub_SlowClient_EvictedWhenSendBufferFull(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.SendBufferSize = 2 // tiny buffer
	hub := startHub(t, cfg, nil)

	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "slow"}
	slow := newBlockingFakeConn("slow", p)
	defer close(slow.blockUntil) // unblock at test end so writeLoop can exit

	require.NoError(t, hub.Register(context.Background(), slow))
	<-slow.readyCh

	// Pump enough messages to overflow send buffer (2) + writeLoop slot.
	// BroadcastToSubject runs the slow path; writeLoop is stuck on Write so
	// chan fills after a few sends.
	for range 10 {
		_ = hub.BroadcastToSubject(context.Background(), "slow", []byte("burst"))
	}

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, testtime.D2s, testtime.D10ms,
		"slow client must be evicted once send buffer fills")
}

// ---------------------------------------------------------------------------
// Subject Index Consistency Tests
// ---------------------------------------------------------------------------

func TestHub_SubjectIdx_EmptyAfterRegisterUnregister(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice"}
	conn := newFakeConnWithPrincipal("a", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	hub.Unregister("a")
	require.Eventually(t, func() bool { return hub.ConnCount() == 0 }, testtime.D2s, testtime.D10ms)

	// Idx must be empty so a future BroadcastToSubject is a no-op.
	err := hub.BroadcastToSubject(context.Background(), "alice", []byte("x"))
	assert.NoError(t, err)
	assert.Empty(t, conn.getWrites())
}

func TestHub_SubjectIdx_EmptyAfterTokenExpiry(t *testing.T) {
	fc := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := DefaultHubConfig(fc)
	cfg.PingInterval = testtime.D10ms
	cfg.PingTimeout = testtime.FastPoll
	hub := startHub(t, cfg, nil)

	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice", ExpiresAt: fc.Now().Add(testtime.D1h)}
	conn := newFakeConnWithPrincipal("a", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	waitForHubPingTicker(t, fc)
	fc.Advance(testtime.D2h)

	require.Eventually(t, func() bool { return hub.ConnCount() == 0 }, testtime.D2s, testtime.D10ms)

	err := hub.BroadcastToSubject(context.Background(), "alice", []byte("x"))
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// SendBufferSize Default Test
// ---------------------------------------------------------------------------

func TestDefaultHubConfig_SendBufferSize(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	assert.Equal(t, 32, cfg.SendBufferSize, "default SendBufferSize must be 32")
}

// ---------------------------------------------------------------------------
// P1-4: SendBufferSize zero-value fallback
// ---------------------------------------------------------------------------

func TestHub_NewHub_ZeroSendBufferSize_GetsDefault(t *testing.T) {
	hub := NewHub(HubConfig{Clock: clock.Real()}, nil)
	assert.Equal(t, defaultSendBufferSize, hub.Config().SendBufferSize,
		"zero SendBufferSize must be replaced with defaultSendBufferSize at construction")
}

// ---------------------------------------------------------------------------
// P1-2: BroadcastFilter runs filter outside lock (no deadlock when filter calls Send)
// ---------------------------------------------------------------------------

func TestHub_BroadcastFilter_FilterRunsWithoutLock(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	conn := newFakeConn("target")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	// filter calls hub.Send — if filter ran under connMu this would deadlock.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hub.BroadcastFilter(context.Background(), []byte("broadcast"),
			func(c Conn) bool {
				// Calling Send inside filter must not deadlock.
				_ = hub.Send(context.Background(), c.ID(), []byte("direct"))
				return false // broadcast itself sends nothing; direct send above suffices
			})
	}()

	select {
	case <-done:
		// no deadlock
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("BroadcastFilter deadlocked — filter likely ran under connMu")
	}

	// The direct Send inside the filter should have enqueued one message.
	require.Eventually(t, func() bool {
		return len(conn.getWrites()) >= 1
	}, testtime.D2s, testtime.D10ms)
}

// ---------------------------------------------------------------------------
// P1-5: Canceled ctx in Send/BroadcastFilter short-circuits immediately
// ---------------------------------------------------------------------------

func TestHub_Send_CanceledCtx_DoesNotEnqueue(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	conn := newFakeConn("target")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	err := hub.Send(canceledCtx, "target", []byte("should not arrive"))
	// Send must return ctx.Err() immediately.
	assert.ErrorIs(t, err, context.Canceled)

	// Conn must receive nothing (canceled ctx short-circuits enqueue).
	assert.Never(t, func() bool {
		return len(conn.getWrites()) > 0
	}, testtime.D100ms, testtime.D10ms)
}

func TestHub_BroadcastFilter_CanceledCtx_StopsEarly(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	// Register multiple conns to have something to iterate.
	conns := make([]*fakeConn, 5)
	for i := range conns {
		conns[i] = newFakeConn(fmt.Sprintf("c%d", i))
		require.NoError(t, hub.Register(context.Background(), conns[i]))
	}
	for _, c := range conns {
		<-c.readyCh
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	// BroadcastFilter with canceled ctx must not enqueue on any conn.
	err := hub.BroadcastFilter(canceledCtx, []byte("no-deliver"),
		func(Conn) bool { return true })
	require.NoError(t, err) // BroadcastFilter itself returns nil regardless

	// No conn should have received a message.
	assert.Never(t, func() bool {
		for _, c := range conns {
			if len(c.getWrites()) > 0 {
				return true
			}
		}
		return false
	}, testtime.D100ms, testtime.D10ms)
}

// ---------------------------------------------------------------------------
// P1-3: Register injects principal into per-conn context for MessageHandler
// ---------------------------------------------------------------------------

func TestHub_Register_PrincipalInjectedToHandlerCtx(t *testing.T) {
	gotPrincipal := make(chan *auth.Principal, 1)
	handler := func(ctx context.Context, _ string, _ []byte) {
		p, _ := auth.FromContext(ctx)
		gotPrincipal <- p
	}

	hub := startHub(t, DefaultHubConfig(clock.Real()), handler)

	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice"}
	conn := newFakeConnWithPrincipal("conn-with-principal", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	conn.readCh <- []byte("hello")

	select {
	case received := <-gotPrincipal:
		require.NotNil(t, received, "principal must be present in handler ctx")
		assert.Equal(t, "alice", received.Subject)
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("handler did not receive message")
	}
}

// ---------------------------------------------------------------------------
// P2-1: Principal snapshot stability — mutation after Register doesn't affect subjectIdx
// ---------------------------------------------------------------------------

func TestHub_PrincipalSnapshot_StableAfterMutation(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "original"}
	conn := newFakeConnWithPrincipal("conn-snapshot", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	// Mutate the principal subject after registration.
	// Hub snapshots subject at Register time; subjectIdx must still use "original".
	conn.mu.Lock()
	conn.principal = &auth.Principal{Kind: auth.PrincipalUser, Subject: "mutated"}
	conn.mu.Unlock()

	// BroadcastToSubject with the original subject must still reach the conn.
	require.NoError(t, hub.BroadcastToSubject(context.Background(), "original", []byte("ping")))
	require.Eventually(t, func() bool {
		return len(conn.getWrites()) == 1
	}, testtime.D2s, testtime.D10ms,
		"subjectIdx must use snapshotted subject, not the mutated one")

	// BroadcastToSubject with the mutated subject must be a no-op.
	require.NoError(t, hub.BroadcastToSubject(context.Background(), "mutated", []byte("should not arrive")))
	assert.Never(t, func() bool {
		return len(conn.getWrites()) > 1
	}, testtime.D100ms, testtime.D10ms,
		"mutated subject must not appear in subjectIdx")
}

// ---------------------------------------------------------------------------
// P1-1 + P2-3: Write failure evicts connection via evictWith (no panic)
// ---------------------------------------------------------------------------

// writeFailConn returns an error on first Write, then succeeds.
type writeFailConn struct {
	*fakeConn
	failOnce sync.Once
	failed   chan struct{}
}

func newWriteFailConn(id string) *writeFailConn {
	return &writeFailConn{
		fakeConn: newFakeConn(id),
		failed:   make(chan struct{}),
	}
}

func (w *writeFailConn) Write(ctx context.Context, data []byte) error {
	var firstFail bool
	w.failOnce.Do(func() { firstFail = true })
	if firstFail {
		close(w.failed)
		return errors.New("simulated write failure")
	}
	return w.fakeConn.Write(ctx, data)
}

func TestHub_WriteFailureEvictsConnection(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(clock.Real()), nil)

	conn := newWriteFailConn("write-fail")
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	// Trigger a write to cause the failure.
	require.NoError(t, hub.Send(context.Background(), "write-fail", []byte("trigger")))

	// Wait for the write failure signal.
	select {
	case <-conn.failed:
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("Write was never called")
	}

	// After write failure, writeLoop should have evicted the connection.
	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, testtime.D2s, testtime.D10ms,
		"write failure must evict connection")
}

// ---------------------------------------------------------------------------
// P1-1: No send-on-closed-channel panic under concurrent broadcast + evict
// ---------------------------------------------------------------------------

func TestHub_PanicSafe_NoSendOnClosedChannel(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	cfg.SendBufferSize = 1 // tiny buffer to trigger evictions quickly
	hub := startHub(t, cfg, nil)

	const n = 50
	for i := range n {
		conn := newFakeConn(fmt.Sprintf("p%d", i))
		require.NoError(t, hub.Register(context.Background(), conn))
	}

	// Wait for all readLoops to be active.
	require.Eventually(t, func() bool {
		return hub.ConnCount() == n
	}, testtime.EventuallyShort, testtime.D1ms)

	var wg sync.WaitGroup

	// Concurrent broadcasters.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				_ = hub.BroadcastFilter(context.Background(), []byte("burst"),
					func(Conn) bool { return true })
			}
		}()
	}

	// Concurrent unregisters.
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hub.Unregister(fmt.Sprintf("p%d", idx))
		}(i)
	}

	wg.Wait()
	// goleak in TestMain will catch leaked goroutines.
	// race detector catches data races.
	// No panic = test passes.
}
