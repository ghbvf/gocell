package rabbitmq

// T10: Connection.Close(ctx) tests.
//
// Connection.Close(ctx context.Context) error implements lifecycle.ContextCloser
// so that bootstrap can register Connection with a shared shutdown budget.
//
// ref: uber-go/fx app.go StopTimeout — shared shutdown budget
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/publisher.go Close signature

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnection_Close_RespectsCtx verifies that Close returns the
// ctx error promptly when ctx is pre-canceled.
func TestConnection_Close_RespectsCtx(t *testing.T) {
	conn, _ := newTestConnection(t)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	start := time.Now()
	err := conn.Close(cancelledCtx)
	elapsed := time.Since(start)

	require.Error(t, err, "Close with pre-canceled ctx must return error")
	assert.Less(t, elapsed, 50*time.Millisecond,
		"Close must return promptly with pre-canceled ctx; got %s", elapsed)
}

// TestConnection_Close_PreCancelledCtxStillStopsReconnectLoop locks down the
// F1 fix: even when ctx is already canceled at entry, Close MUST signal
// closeCh and mark the connection closed so reconnectLoop exits — otherwise
// the goroutine leaks past process-shutdown.
//
// Earlier versions returned ctx.Err() before mu.Lock + close(closeCh),
// leaving reconnectLoop running until process exit. After F1 the local
// state-machine transitions run unconditionally and only the AMQP network
// handshake is gated by ctx.
//
// After the adapterutil.CloseWithDeadline fix (Finding 1): closeFn is always
// invoked even on pre-canceled ctx, so the underlying conn.Close() must be
// called at least once (best-effort admitted close).
func TestConnection_Close_PreCancelledCtxStillStopsReconnectLoop(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Error(t, conn.Close(cancelledCtx),
		"pre-canceled Close must surface ctx error from the network handshake step")

	conn.mu.RLock()
	closed := conn.closed
	conn.mu.RUnlock()
	assert.True(t, closed,
		"pre-canceled Close must still mark the connection closed so reconnectLoop exits")

	// closeCh must be closed — a receive from a closed channel returns
	// immediately with the zero value, so this select fires the closed-case
	// without timing out.
	select {
	case _, ok := <-conn.closeCh:
		assert.False(t, ok,
			"closeCh must be closed by Close so reconnectLoop's select unblocks")
	case <-time.After(50 * time.Millisecond):
		t.Fatal("closeCh was not signaled after pre-canceled Close — reconnectLoop would leak")
	}

	// Finding 1 regression guard: the underlying conn.Close() must be invoked
	// at least once even when ctx is pre-canceled (best-effort admitted close).
	// Give the goroutine a short grace period to complete.
	assert.Eventually(t, func() bool {
		return mockConn.closeCount.Load() >= 1
	}, 200*time.Millisecond, 5*time.Millisecond,
		"underlying conn.Close() must be called at least once even with pre-canceled ctx — broker must receive close frame")
}

// TestConnection_Close_Idempotent verifies that a second Close call
// returns nil immediately (the underlying connection is already closed).
func TestConnection_Close_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)

	ctx := context.Background()
	require.NoError(t, conn.Close(ctx), "first Close must succeed")
	assert.NoError(t, conn.Close(ctx), "second Close must be no-op and return nil")
}

// TestConnection_Close_ClosesConnection verifies that Close delegates
// to the underlying connection close, setting the closed flag.
func TestConnection_Close_ClosesConnection(t *testing.T) {
	conn, _ := newTestConnection(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := conn.Close(ctx)
	require.NoError(t, err, "Close must succeed when ctx has ample budget")

	// After close, the connection must be marked as closed.
	conn.mu.RLock()
	closed := conn.closed
	conn.mu.RUnlock()
	assert.True(t, closed, "connection must be marked closed after Close")
}

// TestConnection_Close_RespectsCtxDeadline verifies that Close honors the
// caller's deadline when the underlying connection.Close is blocked.
//
// Strategy: inject a blocking mockConnection whose Close() blocks on a
// gate channel. We call conn.Close(shortCtx) and assert it returns within
// the budget even though the broker handshake hasn't completed.
//
// ref: uber-go/fx app.go StopTimeout — ctx budget propagated to teardown
func TestConnection_Close_RespectsCtxDeadline(t *testing.T) {
	// Build a blocking mock whose Close() waits until unblocked.
	gate := make(chan struct{})
	released := make(chan struct{})

	blockingConn := &blockingCloseConnection{
		gate:     gate,
		released: released,
	}

	dialFunc := func(url string) (AMQPConnection, error) {
		return blockingConn, nil
	}

	conn, err := NewConnection(Config{
		URL:             "amqp://test:test@localhost:5672/",
		ChannelPoolSize: 1,
		ConfirmTimeout:  2 * time.Second,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)

	// Short budget — should expire before the gate is released.
	budget := 80 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	err = conn.Close(ctx)
	elapsed := time.Since(start)

	// Close must return with a ctx error, not block until gate.
	require.Error(t, err, "Close must return ctx error when budget exceeded")
	assert.LessOrEqual(t, elapsed, budget+50*time.Millisecond,
		"Close must return within budget+tolerance; got %s", elapsed)

	// Release the gate so the background goroutine doesn't leak.
	close(gate)

	// Verify background goroutine exits cleanly.
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Error("background Close goroutine did not exit after gate released")
	}
}

// TestDrainChannelPool_CloseError_LogsDebug covers the slog.Any("error", err)
// branch inside drainChannelPool (connection.go:530-533). The branch fires when
// a pooled channel's Close() returns an error. Drain must continue emptying the
// pool rather than aborting on the first close failure.
func TestDrainChannelPool_CloseError_LogsDebug(t *testing.T) {
	conn, _ := newTestConnection(t)

	// Inject two channels into the pool: both configured to return an error on
	// Close() so the error-log branch is exercised for each drain iteration.
	errClose := errors.New("channel already closed")
	ch1 := newMockChannel()
	ch1.closeErr = errClose
	ch2 := newMockChannel()
	ch2.closeErr = errClose

	conn.channelPool <- ch1
	conn.channelPool <- ch2

	// drainChannelPool is an unexported method; calling it directly is
	// possible because the test is in the same package (rabbitmq).
	conn.drainChannelPool()

	// Both channels must have had Close() called despite the error.
	assert.True(t, ch1.closeCalled, "drainChannelPool must call Close on pooled channels")
	assert.True(t, ch2.closeCalled, "drainChannelPool must call Close on all pooled channels")

	// The pool must be empty after drain.
	select {
	case <-conn.channelPool:
		t.Fatal("channelPool must be empty after drainChannelPool")
	default:
		// expected — pool is empty
	}
}

// blockingCloseConnection is a mock AMQPConnection whose Close() blocks on
// gate until it is closed, then signals released.
type blockingCloseConnection struct {
	mu       sync.Mutex
	isClosed bool
	gate     chan struct{}
	released chan struct{}
}

func (b *blockingCloseConnection) Channel() (AMQPChannel, error) {
	return newMockChannel(), nil
}

func (b *blockingCloseConnection) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	return receiver
}

func (b *blockingCloseConnection) IsClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isClosed
}

func (b *blockingCloseConnection) Close() error {
	<-b.gate // block until test releases
	b.mu.Lock()
	b.isClosed = true
	b.mu.Unlock()
	close(b.released)
	return nil
}
