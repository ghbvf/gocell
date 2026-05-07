package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

func TestCache_SetAndGet(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	err := cache.Set(ctx, "cache:key:1", "hello", testtime.D5min)
	require.NoError(t, err)

	val, err := cache.Get(ctx, "cache:key:1")
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestCache_GetNonExistent(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	val, err := cache.Get(ctx, "cache:missing")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestCache_Delete(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	err := cache.Set(ctx, "cache:del:1", "value", 0)
	require.NoError(t, err)

	err = cache.Delete(ctx, "cache:del:1")
	require.NoError(t, err)

	val, err := cache.Get(ctx, "cache:del:1")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestCache_DeleteNonExistent(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	// Deleting a non-existent key should not error.
	err := cache.Delete(ctx, "cache:nope")
	assert.NoError(t, err)
}

func TestCache_SetError(t *testing.T) {
	mock := newMockCmdable()
	mock.setErr = errMock
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	err := cache.Set(ctx, "cache:err", "val", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}

func TestCache_GetError(t *testing.T) {
	mock := newMockCmdable()
	mock.getErr = errMock
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	val, err := cache.Get(ctx, "cache:err")
	require.Error(t, err)
	assert.Equal(t, "", val)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
}

func TestCache_DeleteError(t *testing.T) {
	mock := newMockCmdable()
	mock.delErr = errMock
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	err := cache.Delete(ctx, "cache:err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_DELETE")
}

func TestCache_ViaClientConstructor(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})
	cache, err := NewCache(client, testNamespace)
	require.NoError(t, err)
	ctx := context.Background()

	err = cache.Set(ctx, "cache:client", "works", 0)
	require.NoError(t, err)

	val, err := cache.Get(ctx, "cache:client")
	require.NoError(t, err)
	assert.Equal(t, "works", val)
}

// TestNewCache_RejectsNilClient pins the constructor's nil-fail-fast
// contract. Symmetric with NewNonceStore — a nil client at composition
// time is a programmer error, not an infrastructure failure.
func TestNewCache_RejectsNilClient(t *testing.T) {
	cache, err := NewCache(nil, testNamespace)

	require.Error(t, err)
	assert.Nil(t, cache)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

// TestNewCache_RejectsInvalidNamespace pins that ns.Validate() fires
// before the nil-client check, so namespace errors surface as
// ErrValidationFailed rather than being shadowed by infra errors.
func TestNewCache_RejectsInvalidNamespace(t *testing.T) {
	cache, err := NewCache(nil, KeyNamespace(""))

	require.Error(t, err)
	assert.Nil(t, cache)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// TestNewCacheFromCmdable_RejectsNilCmdable pins the internal
// cmdable-level constructor's defense-in-depth nil guard. Test
// callers bypass NewCache and call the helper directly, so the
// helper has to police its own input.
func TestNewCacheFromCmdable_RejectsNilCmdable(t *testing.T) {
	cache, err := newCacheFromCmdable(nil, testNamespace)

	require.Error(t, err)
	assert.Nil(t, cache)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

// TestNewCacheFromCmdable_RejectsInvalidNamespace pins that the
// internal helper re-validates ns even when the public NewCache is
// bypassed (tests, future code paths). Symmetric with the public
// constructor's validation guard.
func TestNewCacheFromCmdable_RejectsInvalidNamespace(t *testing.T) {
	cache, err := newCacheFromCmdable(newMockCmdable(), KeyNamespace(""))

	require.Error(t, err)
	assert.Nil(t, cache)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// --- JSON generics tests ---

type testItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestSetJSON_And_GetJSON(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	item := testItem{Name: "widget", Count: 42}
	err := SetJSON(ctx, cache, "json:item:1", item, testtime.D10min)
	require.NoError(t, err)

	got, err := GetJSON[testItem](ctx, cache, "json:item:1")
	require.NoError(t, err)
	assert.Equal(t, item, got)
}

func TestGetJSON_NonExistent(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	got, err := GetJSON[testItem](ctx, cache, "json:missing")
	require.NoError(t, err)
	assert.Equal(t, testItem{}, got)
}

func TestGetJSON_UnmarshalError(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	// Store invalid JSON.
	err := cache.Set(ctx, "json:bad", "not-json", 0)
	require.NoError(t, err)

	_, err = GetJSON[testItem](ctx, cache, "json:bad")
	require.Error(t, err)
	var ecErrUnmarshal *errcode.Error
	require.True(t, errors.As(err, &ecErrUnmarshal))
	assert.Equal(t, ErrAdapterRedisGet, ecErrUnmarshal.Code)
	assert.Contains(t, ecErrUnmarshal.Message+" "+ecErrUnmarshal.InternalMessage, "unmarshal")
}

func TestGetJSON_GetError(t *testing.T) {
	mock := newMockCmdable()
	mock.getErr = errMock
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	_, err := GetJSON[testItem](ctx, cache, "json:err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
}

func TestSetJSON_MarshalError(t *testing.T) {
	mock := newMockCmdable()
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	// chan cannot be marshaled.
	err := SetJSON(ctx, cache, "json:marshal-err", make(chan int), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
	assert.Contains(t, err.Error(), "marshal")
}

func TestSetJSON_SetError(t *testing.T) {
	mock := newMockCmdable()
	mock.setErr = errMock
	cache := mustNewCacheFromCmdable(t, mock)
	ctx := context.Background()

	err := SetJSON(ctx, cache, "json:set-err", testItem{Name: "x"}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}
