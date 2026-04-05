package redis

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface check.
var _ idempotency.Checker = (*IdempotencyChecker)(nil)

func TestIdempotencyChecker_MarkAndCheck(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// Not processed initially.
	ok, err := ic.IsProcessed(ctx, "idem:test:1")
	require.NoError(t, err)
	assert.False(t, ok)

	// Mark as processed.
	err = ic.MarkProcessed(ctx, "idem:test:1", 24*time.Hour)
	require.NoError(t, err)

	// Now should be processed.
	ok, err = ic.IsProcessed(ctx, "idem:test:1")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestIdempotencyChecker_MarkIdempotent(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// Mark twice - second should be no-op (SetNX returns false but no error).
	err := ic.MarkProcessed(ctx, "idem:test:2", 24*time.Hour)
	require.NoError(t, err)

	err = ic.MarkProcessed(ctx, "idem:test:2", 24*time.Hour)
	require.NoError(t, err)
}

func TestIdempotencyChecker_IsProcessed_GetError(t *testing.T) {
	mock := newMockCmdable()
	mock.getErr = errMock
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	ok, err := ic.IsProcessed(ctx, "idem:test:err")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
}

func TestIdempotencyChecker_MarkProcessed_SetNXError(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	err := ic.MarkProcessed(ctx, "idem:test:err", 24*time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}

func TestIdempotencyChecker_ViaClientConstructor(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})
	ic := NewIdempotencyChecker(client)
	ctx := context.Background()

	ok, err := ic.IsProcessed(ctx, "idem:test:client")
	require.NoError(t, err)
	assert.False(t, ok)
}
