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

// nonceTestNamespace pins the namespace used by NonceStore unit tests.
// Distinct from the package-wide testNamespace so any cross-prefix
// regression (e.g. an accidental nonce key landing under "testns:")
// shows up as a missed lookup.
const nonceTestNamespace KeyNamespace = "servicetoken-nonce"

func TestNonceStore_FirstUseSucceeds(t *testing.T) {
	store := mustNewNonceStoreFromCmdable(t, newMockCmdable())

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
}

func TestNewNonceStore_RejectsNilClient(t *testing.T) {
	store, err := NewNonceStore(nil, nonceTestNamespace, auth.ServiceTokenNonceTTL)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

func TestNewNonceStore_UsesClientCmdable(t *testing.T) {
	client := newClientFromCmdable(newMockCmdable(), Config{Addr: "redis:6379"})

	store, err := NewNonceStore(client, nonceTestNamespace, auth.ServiceTokenNonceTTL)

	require.NoError(t, err)
	require.NotNil(t, store)
	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
}

func TestNonceStore_ReplayReturnsAuthNonceReused(t *testing.T) {
	store := mustNewNonceStoreFromCmdable(t, newMockCmdable())

	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))
	err := store.CheckAndMark(context.Background(), "nonce-a")

	assert.ErrorIs(t, err, auth.ErrNonceReused)
}

// TestNonceStore_NamespacePrefix_KeyDerivation pins the wire-level key shape
// produced by CheckAndMark. The mock store is keyed by exactly the string
// the cmdable sees, so we can assert the namespace prefix without a real
// Redis instance.
func TestNonceStore_NamespacePrefix_KeyDerivation(t *testing.T) {
	mock := newMockCmdable()
	store := mustNewNonceStoreFromCmdable(t, mock)

	start := time.Now()
	require.NoError(t, store.CheckAndMark(context.Background(), "nonce-a"))

	mock.mu.Lock()
	entry, ok := mock.store[string(nonceTestNamespace)+":nonce-a"]
	mock.mu.Unlock()
	require.True(t, ok, "expected key under namespace prefix; mock.store keys: %v", mockKeys(mock))
	require.False(t, entry.expiry.IsZero())
	assert.WithinDuration(t, start.Add(auth.ServiceTokenNonceTTL), entry.expiry, time.Second)
}

func TestNonceStore_RedisErrorWrapped(t *testing.T) {
	mock := newMockCmdable()
	mock.setNXErr = errMock
	store := mustNewNonceStoreFromCmdable(t, mock)

	err := store.CheckAndMark(context.Background(), "nonce-a")

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.True(t, errors.Is(err, errMock))
}

func TestNonceStore_KindDistributed(t *testing.T) {
	store := mustNewNonceStoreFromCmdable(t, newMockCmdable())

	assert.Equal(t, auth.NonceStoreKindDistributed, store.Kind())
}

func TestNonceStore_RejectsReplayUnsafeTTL(t *testing.T) {
	_, err := newNonceStoreFromCmdable(newMockCmdable(), nonceTestNamespace, auth.ServiceTokenNonceTTL-time.Nanosecond)

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.Contains(t, err.Error(), "ServiceTokenNonceTTL")
}

func TestNonceStore_RejectsNilCmdable(t *testing.T) {
	store, err := newNonceStoreFromCmdable(nil, nonceTestNamespace, auth.ServiceTokenNonceTTL)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

func TestNonceStore_RejectsNonPositiveTTL(t *testing.T) {
	store, err := newNonceStoreFromCmdable(newMockCmdable(), nonceTestNamespace, 0)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisSet, ec.Code)
	assert.Contains(t, err.Error(), "positive")
}

func TestNonceStore_RejectsInvalidNamespace(t *testing.T) {
	_, err := newNonceStoreFromCmdable(newMockCmdable(), KeyNamespace(""), auth.ServiceTokenNonceTTL)

	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// mockKeys returns a snapshot of mock.store keys for diagnostic output.
func mockKeys(m *mockCmdable) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.store))
	for k := range m.store {
		out = append(out, k)
	}
	return out
}
