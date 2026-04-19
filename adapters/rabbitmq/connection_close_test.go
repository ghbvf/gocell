package rabbitmq

// T10: Connection.CloseCtx(ctx) tests.
//
// The existing Connection.Close() has no context parameter. Part 4 adds
// CloseCtx(ctx) so bootstrap can register Connection as a lifecycle.ContextCloser.
//
// ref: uber-go/fx app.go StopTimeout — shared shutdown budget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnection_CloseCtx_RespectsCtx verifies that CloseCtx returns the
// ctx error promptly when ctx is pre-cancelled.
func TestConnection_CloseCtx_RespectsCtx(t *testing.T) {
	conn, _ := newTestConnection(t)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	err := conn.CloseCtx(cancelledCtx)
	elapsed := time.Since(start)

	require.Error(t, err, "CloseCtx with pre-cancelled ctx must return error")
	assert.Less(t, elapsed, 50*time.Millisecond,
		"CloseCtx must return promptly with pre-cancelled ctx; got %s", elapsed)
}

// TestConnection_CloseCtx_Idempotent verifies that a second CloseCtx call
// returns nil immediately (the underlying connection is already closed).
func TestConnection_CloseCtx_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)

	ctx := context.Background()
	require.NoError(t, conn.CloseCtx(ctx), "first CloseCtx must succeed")
	assert.NoError(t, conn.CloseCtx(ctx), "second CloseCtx must be no-op and return nil")
}

// TestConnection_CloseCtx_ClosesConnection verifies that CloseCtx delegates
// to the underlying connection close, setting the closed flag.
func TestConnection_CloseCtx_ClosesConnection(t *testing.T) {
	conn, _ := newTestConnection(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := conn.CloseCtx(ctx)
	require.NoError(t, err, "CloseCtx must succeed when ctx has ample budget")

	// After close, the connection must refuse new channels.
	conn.mu.RLock()
	closed := conn.closed
	conn.mu.RUnlock()
	assert.True(t, closed, "connection must be marked closed after CloseCtx")
}
