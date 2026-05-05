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
	require.NoError(t, hub.Send(context.Background(), "dup", []byte("hello")))
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

func TestDefaultHubConfig(t *testing.T) {
	cfg := DefaultHubConfig(clock.Real())
	assert.Equal(t, testtime.D30s, cfg.PingInterval)
	assert.Equal(t, testtime.D5s, cfg.PingTimeout)
	assert.Equal(t, int64(64*1024), cfg.ReadLimit)
	assert.Equal(t, 2, cfg.PingMissMax)
}

func TestNewHub_PreservesExplicitConfig(t *testing.T) {
	cfg := HubConfig{
		PingInterval:   testtime.D1s,
		PingTimeout:    testtime.D250ms,
		ReadLimit:      1024,
		PingMissMax:    5,
		MaxConnections: 9,
		Clock:          clock.Real(),
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
func (s *stuckConn) Write(_ context.Context, _ []byte) error    { return nil }
func (s *stuckConn) Principal() *auth.Principal                 { return nil }
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
	time.Sleep(testtime.D50ms) //archtest:allow:test-sleep negative test: bob must not receive within window
	assert.Empty(t, b.getWrites())
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
	time.Sleep(testtime.D50ms) //archtest:allow:test-sleep negative window
	assert.Empty(t, b.getWrites())
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
		ExpiresAt: fc.Now().Add(time.Hour), // 1h from now
	}
	conn := newFakeConnWithPrincipal("expiring", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh
	require.Equal(t, 1, hub.ConnCount())

	// Advance clock past expiry; next ping tick must evict.
	fc.Advance(2 * time.Hour)

	require.Eventually(t, func() bool {
		return hub.ConnCount() == 0 && conn.isClosed()
	}, testtime.D2s, testtime.D10ms)
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

	// Advance clock far into future. Conn must remain.
	fc.Advance(100 * 365 * 24 * time.Hour)

	time.Sleep(testtime.D50ms) //archtest:allow:test-sleep negative test: must NOT evict
	assert.Equal(t, 1, hub.ConnCount())
	assert.False(t, conn.isClosed())
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

	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "alice", ExpiresAt: fc.Now().Add(time.Hour)}
	conn := newFakeConnWithPrincipal("a", p)
	require.NoError(t, hub.Register(context.Background(), conn))
	<-conn.readyCh

	fc.Advance(2 * time.Hour)

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
