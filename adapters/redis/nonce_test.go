package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestNonceStore_FirstUseSucceeds(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), auth.ServiceTokenNonceTTL)
	require.NoError(t, err)

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
}

func TestNewNonceStore_RejectsNilClient(t *testing.T) {
	store, err := NewNonceStore(nil, auth.ServiceTokenNonceTTL)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

func TestNewNonceStore_UsesClientCmdable(t *testing.T) {
	client := newClientFromCmdable(newMockCmdable(), Config{Addr: "redis:6379"})

	store, err := NewNonceStore(client, auth.ServiceTokenNonceTTL)

	require.NoError(t, err)
	require.NotNil(t, store)
	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
}

func TestNonceStore_ReplayReturnsAuthNonceReused(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), auth.ServiceTokenNonceTTL)
	require.NoError(t, err)

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
	err = store.CheckAndMark(context.Background(), "nonce-a")

	assert.ErrorIs(t, err, auth.ErrNonceReused)
}

func TestNonceStore_ServiceTokenTTLUsesAuthConstant(t *testing.T) {
	mock := newMockCmdable()
	store, err := newNonceStoreFromCmdable(mock, auth.ServiceTokenNonceTTL)
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))

	mock.mu.Lock()
	entry, ok := mock.store[serviceTokenNoncePrefix+"nonce-a"]
	mock.mu.Unlock()
	require.True(t, ok)
	require.False(t, entry.expiry.IsZero())
	assert.WithinDuration(t, start.Add(auth.ServiceTokenNonceTTL), entry.expiry, time.Second)
}

func TestNonceStore_RedisErrorWrapped(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	store, err := newNonceStoreFromCmdable(mock, auth.ServiceTokenNonceTTL)
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-a")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.True(t, errors.Is(err, errMock))
}

func TestNonceStore_KindDistributed(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), auth.ServiceTokenNonceTTL)
	require.NoError(t, err)

	assert.Equal(t, auth.NonceStoreKindDistributed, store.Kind())
}

func TestNonceStore_RejectsReplayUnsafeTTL(t *testing.T) {
	_, err := newNonceStoreFromCmdable(newMockCmdable(), auth.ServiceTokenNonceTTL-time.Nanosecond)

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.Contains(t, err.Error(), "ServiceTokenNonceTTL")
}

func TestNonceStore_RejectsNilCmdable(t *testing.T) {
	store, err := newNonceStoreFromCmdable(nil, auth.ServiceTokenNonceTTL)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

func TestNonceStore_RejectsNonPositiveTTL(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), 0)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.Contains(t, err.Error(), "positive")
}
