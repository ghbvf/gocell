package websocket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConn implements Conn for lifecycle tests without network.
type fakeConn struct {
	id     string
	readCh chan []byte // send data to simulate client messages; close to end Read

	mu      sync.Mutex
	closed  bool
	writes  [][]byte
	closeCh chan struct{} // closed on Close() to unblock Read
}

func newFakeConn(id string) *fakeConn {
	return &fakeConn{
		id:      id,
		readCh:  make(chan []byte, 10),
		closeCh: make(chan struct{}),
	}
}

func (f *fakeConn) ID() string { return f.id }

func (f *fakeConn) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("closed")
	}
	return nil
}

func (f *fakeConn) Read(_ context.Context) ([]byte, error) {
	select {
	case data, ok := <-f.readCh:
		if !ok {
			return nil, errors.New("read channel closed")
		}
		return data, nil
	case <-f.closeCh:
		return nil, errors.New("connection closed")
	}
}

func (f *fakeConn) Write(_ context.Context, data []byte) error {
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

// --- Lifecycle Tests ---

func TestHub_StopUnblocksStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	startErr := make(chan error, 1)
	go func() {
		startErr <- hub.Start(context.Background())
	}()

	// Give Start time to begin.
	time.Sleep(50 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))

	// Start should have returned nil (stopped normally).
	select {
	case err := <-startErr:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestHub_StopBeforeStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	// Register a connection before Start.
	conn := newFakeConn("pre-start")
	require.NoError(t, hub.Register(conn))
	assert.Equal(t, 1, hub.ConnCount())

	// Stop without Start — should close connections and be a no-op.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))

	assert.True(t, conn.isClosed())
	assert.Equal(t, 0, hub.ConnCount())

	// Hub should still be usable: Start → register → Stop.
	startErr := make(chan error, 1)
	go func() {
		startErr <- hub.Start(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)

	conn2 := newFakeConn("after-restart")
	require.NoError(t, hub.Register(conn2))
	assert.Equal(t, 1, hub.ConnCount())

	stopCtx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	require.NoError(t, hub.Stop(stopCtx2))

	select {
	case err := <-startErr:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after second Stop")
	}

	assert.True(t, conn2.isClosed())
}

func TestHub_StartStopStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	// First cycle.
	startErr := make(chan error, 1)
	go func() {
		startErr <- hub.Start(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)

	conn1 := newFakeConn("cycle1")
	require.NoError(t, hub.Register(conn1))

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, hub.Stop(stopCtx))
	assert.NoError(t, <-startErr)
	assert.True(t, conn1.isClosed())

	// Second cycle — same Hub instance.
	go func() {
		startErr <- hub.Start(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)

	conn2 := newFakeConn("cycle2")
	require.NoError(t, hub.Register(conn2))
	assert.Equal(t, 1, hub.ConnCount())

	require.NoError(t, hub.Send(context.Background(), "cycle2", []byte("hello")))
	assert.Equal(t, [][]byte{[]byte("hello")}, conn2.getWrites())

	stopCtx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	require.NoError(t, hub.Stop(stopCtx2))
	assert.NoError(t, <-startErr)
	assert.True(t, conn2.isClosed())
}

func TestHub_DoubleStart(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	go func() {
		_ = hub.Start(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)

	// Second Start should fail.
	err := hub.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	// Cleanup.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hub.Stop(stopCtx)
}

func TestHub_RegisterDuringStop(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	go func() {
		_ = hub.Start(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)

	// Manually set stopping to simulate mid-Stop.
	hub.stateMu.Lock()
	hub.stopping = true
	hub.stateMu.Unlock()

	conn := newFakeConn("rejected")
	err := hub.Register(conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopping")
	assert.True(t, conn.isClosed(), "rejected conn should be closed")

	// Reset and cleanup.
	hub.stateMu.Lock()
	hub.stopping = false
	hub.stateMu.Unlock()

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hub.Stop(stopCtx)
}

// --- Functional Tests (with fakeConn) ---

func TestHub_BroadcastFake(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)

	go func() { _ = hub.Start(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	c1 := newFakeConn("c1")
	c2 := newFakeConn("c2")
	require.NoError(t, hub.Register(c1))
	require.NoError(t, hub.Register(c2))

	hub.Broadcast(context.Background(), []byte("hello all"))

	assert.Equal(t, [][]byte{[]byte("hello all")}, c1.getWrites())
	assert.Equal(t, [][]byte{[]byte("hello all")}, c2.getWrites())

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hub.Stop(stopCtx)
}

func TestHub_SendNotFound(t *testing.T) {
	hub := NewHub(DefaultHubConfig(), nil)
	err := hub.Send(context.Background(), "nonexistent", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHub_MessageHandlerFake(t *testing.T) {
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

	hub := NewHub(DefaultHubConfig(), handler)
	go func() { _ = hub.Start(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	conn := newFakeConn("sender")
	require.NoError(t, hub.Register(conn))

	// Simulate client sending a message.
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

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hub.Stop(stopCtx)
}

func TestDefaultHubConfig(t *testing.T) {
	cfg := DefaultHubConfig()
	assert.Equal(t, 30*time.Second, cfg.PingInterval)
	assert.Equal(t, 5*time.Second, cfg.PingTimeout)
	assert.Equal(t, int64(64*1024), cfg.ReadLimit)
}
