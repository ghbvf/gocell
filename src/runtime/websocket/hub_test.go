package websocket

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---------------------------------------------------------------------------
// fakeConn — network-free Conn for unit tests
// ---------------------------------------------------------------------------

type fakeConn struct {
	id      string
	readCh  chan []byte    // send data to simulate client messages; close to end Read
	closeCh chan struct{}  // closed on Close() to unblock Read
	readyCh chan struct{}  // closed on first Read call (replaces time.Sleep)

	mu         sync.Mutex
	closed     bool
	writes     [][]byte
	readyOnce  sync.Once
	pingErr    error         // configurable: non-nil makes Ping fail
	writeDelay time.Duration // configurable: simulates slow writes
}

func newFakeConn(id string) *fakeConn {
	return &fakeConn{
		id:      id,
		readCh:  make(chan []byte, 10),
		closeCh: make(chan struct{}),
		readyCh: make(chan struct{}),
	}
}

func (f *fakeConn) ID() string { return f.id }

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
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("closed")
	}
	if f.writeDelay > 0 {
		delay := f.writeDelay
		f.mu.Unlock()
		time.Sleep(delay)
		f.mu.Lock()
		if f.closed {
			return errors.New("closed")
		}
	}
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.closeCh)
	return nil
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
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Stop(stopCtx)
		<-startErr
	})
	// Wait until hub is running.
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)
	return hub
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestHub_StopUnblocksStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()

	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))

	select {
	case err := <-startErr:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestHub_DoubleStart(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	err := hub.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestHub_DoubleStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))
	<-startErr

	err := hub.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestHub_StopBeforeStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	require.NoError(t, hub.Stop(context.Background()))
	assert.Equal(t, stateStopped, hub.state.Load())
}

func TestHub_StartAfterStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	require.NoError(t, hub.Stop(context.Background()))

	err := hub.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestHub_StopTimeout(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)

	// Register a conn whose Close is a no-op (readLoop never exits).
	stuck := &stuckConn{id: "stuck", closeCh: make(chan struct{})}
	require.NoError(t, hub.Register(stuck))

	// Stop with very short timeout.
	stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := hub.Stop(stopCtx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, stateStopped, hub.state.Load())

	// Unblock the stuck conn so goroutine exits for goleak.
	close(stuck.closeCh)
	<-startErr
}

func TestHub_ExternalContextCancel(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(ctx) }()

	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)

	cancel()

	select {
	case err := <-startErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancel")
	}

	// Cleanup: Stop to reach terminal state.
	_ = hub.Stop(context.Background())
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

	hub := startHub(t, DefaultHubConfig(), handler)

	conn := newFakeConn("sender")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	conn.readCh <- []byte("hello server")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMsg != ""
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "sender", gotID)
	assert.Equal(t, "hello server", gotMsg)
	mu.Unlock()
}

func TestHub_RegisterDuringStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)

	// Force state to stopping to simulate mid-Stop window.
	hub.state.Store(stateStopping)

	conn := newFakeConn("rejected")
	err := hub.Register(conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopping")
	assert.True(t, conn.isClosed())

	// Reset for cleanup.
	hub.state.Store(stateRunning)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hub.Stop(stopCtx)
	<-startErr
}

func TestHub_RegisterOnStoppedHub(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	_ = hub.Stop(context.Background())

	conn := newFakeConn("late")
	err := hub.Register(conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
	assert.True(t, conn.isClosed())
}

func TestHub_Unregister(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	conn := newFakeConn("c1")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	assert.Equal(t, 1, hub.ConnCount())
	hub.Unregister("c1")

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, time.Second, 10*time.Millisecond)
	assert.True(t, conn.isClosed())
}

func TestHub_UnregisterIdempotent(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	conn := newFakeConn("c1")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	hub.Unregister("c1")
	hub.Unregister("c1") // should not panic

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, time.Second, 10*time.Millisecond)
}

// ---------------------------------------------------------------------------
// Concurrency Tests
// ---------------------------------------------------------------------------

func TestHub_RegisterStopRace(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	startErr := make(chan error, 1)
	go func() { startErr <- hub.Start(context.Background()) }()
	require.Eventually(t, func() bool {
		return hub.state.Load() == stateRunning
	}, time.Second, time.Millisecond)

	const n = 100
	var registerWg sync.WaitGroup
	registerWg.Add(n)
	for i := range n {
		go func(idx int) {
			defer registerWg.Done()
			c := newFakeConn(fmt.Sprintf("race-%d", idx))
			_ = hub.Register(c)
		}(i)
	}

	// Stop concurrently with registrations.
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hub.Stop(stopCtx)

	registerWg.Wait()
	<-startErr
	// goleak.VerifyTestMain catches any leaked goroutines.
}

func TestHub_ConcurrentBroadcast(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	conns := make([]*fakeConn, 5)
	for i := range conns {
		c := newFakeConn("bc" + string(rune('0'+i)))
		conns[i] = c
		require.NoError(t, hub.Register(c))
		<-c.readyCh
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.Broadcast(context.Background(), []byte("msg"))
		}()
	}
	wg.Wait()

	for _, c := range conns {
		assert.NotEmpty(t, c.getWrites())
	}
}

// ---------------------------------------------------------------------------
// Functional Tests
// ---------------------------------------------------------------------------

func TestHub_Broadcast(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	c1 := newFakeConn("c1")
	c2 := newFakeConn("c2")
	require.NoError(t, hub.Register(c1))
	require.NoError(t, hub.Register(c2))
	<-c1.readyCh
	<-c2.readyCh

	hub.Broadcast(context.Background(), []byte("hello all"))

	assert.Equal(t, [][]byte{[]byte("hello all")}, c1.getWrites())
	assert.Equal(t, [][]byte{[]byte("hello all")}, c2.getWrites())
}

func TestHub_BroadcastSlowConn(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	fast := newFakeConn("fast")
	slow := newFakeConn("slow")
	slow.writeDelay = 200 * time.Millisecond

	require.NoError(t, hub.Register(fast))
	require.NoError(t, hub.Register(slow))
	<-fast.readyCh
	<-slow.readyCh

	start := time.Now()
	hub.Broadcast(context.Background(), []byte("data"))
	elapsed := time.Since(start)

	assert.Equal(t, [][]byte{[]byte("data")}, fast.getWrites())
	assert.Equal(t, [][]byte{[]byte("data")}, slow.getWrites())
	assert.Less(t, elapsed, 400*time.Millisecond, "broadcast should be concurrent")
}

func TestHub_Send(t *testing.T) {
	hub := startHub(t, DefaultHubConfig(), nil)

	conn := newFakeConn("target")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	require.NoError(t, hub.Send(context.Background(), "target", []byte("direct")))
	assert.Equal(t, [][]byte{[]byte("direct")}, conn.getWrites())
}

func TestHub_SendNotFound(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
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

	hub := startHub(t, DefaultHubConfig(), handler)

	conn := newFakeConn("h1")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	conn.readCh <- []byte("payload")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotMsg != ""
	}, time.Second, 10*time.Millisecond)

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
	cfg := DefaultHubConfig()
	cfg.PingInterval = 10 * time.Millisecond
	cfg.PingTimeout = 5 * time.Millisecond
	cfg.PingMissMax = 3

	hub := startHub(t, cfg, nil)

	conn := newFakeConn("pinger")
	conn.pingErr = errors.New("timeout")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0
	}, 2*time.Second, 10*time.Millisecond)
	assert.True(t, conn.isClosed())
}

func TestHub_PingMissReset(t *testing.T) {
	cfg := DefaultHubConfig()
	cfg.PingInterval = 10 * time.Millisecond
	cfg.PingTimeout = 5 * time.Millisecond
	cfg.PingMissMax = 3

	hub := startHub(t, cfg, nil)

	conn := newFakeConn("resilient")
	conn.pingErr = errors.New("fail")
	require.NoError(t, hub.Register(conn))
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
	}, 2*time.Second, 5*time.Millisecond)

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
	}, 2*time.Second, 5*time.Millisecond)

	assert.Equal(t, 1, hub.ConnCount(), "connection should survive after healing")
}

func TestHub_PingLoopRunsOnInterval(t *testing.T) {
	cfg := DefaultHubConfig()
	cfg.PingInterval = 10 * time.Millisecond
	cfg.PingTimeout = 5 * time.Millisecond

	hub := startHub(t, cfg, nil)

	conn := newFakeConn("counter")
	require.NoError(t, hub.Register(conn))
	<-conn.readyCh

	// Wait long enough for multiple pings, verify conn is still alive.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, hub.ConnCount(), "connection should survive multiple pings")
}

// ---------------------------------------------------------------------------
// Config Tests
// ---------------------------------------------------------------------------

func TestDefaultHubConfig(t *testing.T) {
	cfg := DefaultHubConfig()
	assert.Equal(t, 30*time.Second, cfg.PingInterval)
	assert.Equal(t, 5*time.Second, cfg.PingTimeout)
	assert.Equal(t, int64(64*1024), cfg.ReadLimit)
	assert.Equal(t, 2, cfg.PingMissMax)
}

func TestNewHub_NilHandler(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	assert.NotNil(t, hub.handler, "nil handler should be replaced with noop")
	hub.handler(context.Background(), "test", []byte("data"))
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

func (s *stuckConn) ID() string { return s.id }
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
func (s *stuckConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
	}
	// Intentionally do NOT close closeCh — simulates a conn that won't unblock.
	return nil
}

// Compile-time interface checks.
var _ Conn = (*fakeConn)(nil)
var _ Conn = (*stuckConn)(nil)

// Suppress unused import warnings.
var _ atomic.Int32
