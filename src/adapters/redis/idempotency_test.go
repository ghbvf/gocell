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

func TestIdempotencyChecker_TryProcess_FirstCall(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// First call: key does not exist, should return true (caller should process).
	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try:1", 24*time.Hour)
	require.NoError(t, err)
	assert.True(t, shouldProcess, "first TryProcess should return true")

	// Verify key is now marked as processed via IsProcessed.
	ok, err := ic.IsProcessed(ctx, "idem:test:try:1")
	require.NoError(t, err)
	assert.True(t, ok, "key should be processed after TryProcess")
}

func TestIdempotencyChecker_TryProcess_Duplicate(t *testing.T) {
	mock := newMockCmdable()
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	// First call succeeds.
	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try:2", 24*time.Hour)
	require.NoError(t, err)
	assert.True(t, shouldProcess)

	// Second call: key already exists, should return false.
	shouldProcess, err = ic.TryProcess(ctx, "idem:test:try:2", 24*time.Hour)
	require.NoError(t, err)
	assert.False(t, shouldProcess, "duplicate TryProcess should return false")
}

func TestIdempotencyChecker_TryProcess_SetNXError(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	ic := newIdempotencyCheckerFromCmdable(mock)
	ctx := context.Background()

	shouldProcess, err := ic.TryProcess(ctx, "idem:test:try:err", 24*time.Hour)
	require.Error(t, err)
	assert.False(t, shouldProcess)
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
