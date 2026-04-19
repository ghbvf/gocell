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
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnection_Close_RespectsCtx verifies that Close returns the
// ctx error promptly when ctx is pre-cancelled.
func TestConnection_Close_RespectsCtx(t *testing.T) {
	conn, _ := newTestConnection(t)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	err := conn.Close(cancelledCtx)
	elapsed := time.Since(start)

	require.Error(t, err, "Close with pre-cancelled ctx must return error")
	assert.Less(t, elapsed, 50*time.Millisecond,
		"Close must return promptly with pre-cancelled ctx; got %s", elapsed)
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

// TestConnection_Close_RespectsCtxDeadline verifies that Close honours the
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
