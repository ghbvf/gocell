package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_SetAndGet(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	err := cache.Set(ctx, "cache:key:1", "hello", 5*time.Minute)
	require.NoError(t, err)

	val, err := cache.Get(ctx, "cache:key:1")
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestCache_GetNonExistent(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	val, err := cache.Get(ctx, "cache:missing")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestCache_Delete(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
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
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	// Deleting a non-existent key should not error.
	err := cache.Delete(ctx, "cache:nope")
	assert.NoError(t, err)
}

func TestCache_SetError(t *testing.T) {
	mock := newMockCmdable()
	mock.setErr = errMock
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	err := cache.Set(ctx, "cache:err", "val", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}

func TestCache_GetError(t *testing.T) {
	mock := newMockCmdable()
	mock.getErr = errMock
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	val, err := cache.Get(ctx, "cache:err")
	require.Error(t, err)
	assert.Equal(t, "", val)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
}

func TestCache_DeleteError(t *testing.T) {
	mock := newMockCmdable()
	mock.delErr = errMock
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	err := cache.Delete(ctx, "cache:err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}

func TestCache_ViaClientConstructor(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})
	cache := NewCache(client)
	ctx := context.Background()

	err := cache.Set(ctx, "cache:client", "works", 0)
	require.NoError(t, err)

	val, err := cache.Get(ctx, "cache:client")
	require.NoError(t, err)
	assert.Equal(t, "works", val)
}

// --- JSON generics tests ---

type testItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestSetJSON_And_GetJSON(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	item := testItem{Name: "widget", Count: 42}
	err := SetJSON(ctx, cache, "json:item:1", item, 10*time.Minute)
	require.NoError(t, err)

	got, err := GetJSON[testItem](ctx, cache, "json:item:1")
	require.NoError(t, err)
	assert.Equal(t, item, got)
}

func TestGetJSON_NonExistent(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	got, err := GetJSON[testItem](ctx, cache, "json:missing")
	require.NoError(t, err)
	assert.Equal(t, testItem{}, got)
}

func TestGetJSON_UnmarshalError(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	// Store invalid JSON.
	err := cache.Set(ctx, "json:bad", "not-json", 0)
	require.NoError(t, err)

	_, err = GetJSON[testItem](ctx, cache, "json:bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestGetJSON_GetError(t *testing.T) {
	mock := newMockCmdable()
	mock.getErr = errMock
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	_, err := GetJSON[testItem](ctx, cache, "json:err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_GET")
}

func TestSetJSON_MarshalError(t *testing.T) {
	mock := newMockCmdable()
	cache := newCacheFromCmdable(mock)
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
	cache := newCacheFromCmdable(mock)
	ctx := context.Background()

	err := SetJSON(ctx, cache, "json:set-err", testItem{Name: "x"}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
}
