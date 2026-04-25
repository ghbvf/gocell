package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonceStore_FirstUseSucceeds(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), time.Minute)
	require.NoError(t, err)

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
}

func TestNonceStore_ReplayReturnsAuthNonceReused(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), time.Minute)
	require.NoError(t, err)

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
	err = store.CheckAndMark(context.Background(), "nonce-a")

	assert.ErrorIs(t, err, auth.ErrNonceReused)
}

func TestNonceStore_TTLAllowsReuse(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), 10*time.Millisecond)
	require.NoError(t, err)

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
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
	store, err := newNonceStoreFromCmdable(mock, time.Minute)
	require.NoError(t, err)

	err = store.CheckAndMark(context.Background(), "nonce-a")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.True(t, errors.Is(err, errMock))
}

func TestNonceStore_KindDistributed(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), time.Minute)
	require.NoError(t, err)

	assert.Equal(t, auth.NonceStoreKindDistributed, store.Kind())
}
