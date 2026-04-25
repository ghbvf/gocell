package main

import (
	"context"
	"testing"
	"time"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRedisConfigFromEnv_RealMultiPodMissingAddrFailFast(t *testing.T) {
	t.Setenv(envRedisAddr, "")
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	_, configured, err := loadRedisConfigFromEnv(topo)

	require.Error(t, err)
	assert.False(t, configured)
	assertErrCode(t, err, errcode.ErrValidationFailed)
	assert.Contains(t, err.Error(), envRedisAddr)
	assert.Contains(t, err.Error(), "multi-pod")
}

func TestLoadRedisConfigFromEnv_InvalidDBFailFast(t *testing.T) {
	tests := []struct {
		name string
		db   string
	}{
		{name: "not integer", db: "abc"},
		{name: "negative", db: "-1"},
	}

	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envRedisAddr, "redis:6379")
			t.Setenv(envRedisDB, tc.db)

			_, configured, err := loadRedisConfigFromEnv(topo)

			require.Error(t, err)
			assert.False(t, configured)
			assertErrCode(t, err, errcode.ErrValidationFailed)
			assert.Contains(t, err.Error(), envRedisDB)
			assert.Contains(t, err.Error(), tc.db)
		})
	}
}

func TestBuildReplayDependencies_RealSinglePodUsesInMemory(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:               "real",
		StorageBackend:            "postgres",
		SinglePodReplayProtection: true,
	}

	nonceStore, err := buildServiceNonceStore(topo, nil)
	require.NoError(t, err)
	assert.Equal(t, auth.NonceStoreKindInMemory, nonceStore.Kind())
	inMemoryNonceStore, ok := nonceStore.(*auth.InMemoryNonceStore)
	require.True(t, ok)
	assert.Equal(t, auth.ServiceTokenNonceTTL, inMemoryNonceStore.MaxAge())

	claimer, kind, err := buildConsumerClaimer(topo, nil)
	require.NoError(t, err)
	assert.Equal(t, consumerClaimerKindInMemory, kind)
	assert.IsType(t, &idempotency.InMemClaimer{}, claimer)
}

func TestBuildReplayDependencies_RealMultiPodConfiguredRedisUsesDistributedStores(t *testing.T) {
	topo := bootstrap.Topology{
		AdapterMode:    "real",
		StorageBackend: "postgres",
	}
	client := new(adapterredis.Client)
	var gotNonceClient *adapterredis.Client
	var gotNonceTTL time.Duration
	var gotClaimerClient *adapterredis.Client

	originalNonceStoreFactory := newRedisNonceStore
	originalClaimerFactory := newRedisIdempotencyClaimer
	t.Cleanup(func() {
		newRedisNonceStore = originalNonceStoreFactory
		newRedisIdempotencyClaimer = originalClaimerFactory
	})

	newRedisNonceStore = func(c *adapterredis.Client, ttl time.Duration) (auth.NonceStore, error) {
		gotNonceClient = c
		gotNonceTTL = ttl
		return fakeDistributedNonceStore{}, nil
	}
	newRedisIdempotencyClaimer = func(c *adapterredis.Client) idempotency.Claimer {
		gotClaimerClient = c
		return fakeDistributedClaimer{}
	}

	nonceStore, err := buildServiceNonceStore(topo, client)
	require.NoError(t, err)
	assert.Same(t, client, gotNonceClient)
	assert.Equal(t, auth.ServiceTokenNonceTTL, gotNonceTTL)
	assert.Equal(t, auth.NonceStoreKindDistributed, nonceStore.Kind())

	claimer, kind, err := buildConsumerClaimer(topo, client)
	require.NoError(t, err)
	assert.Same(t, client, gotClaimerClient)
	assert.Equal(t, consumerClaimerKindDistributed, kind)
	assert.IsType(t, fakeDistributedClaimer{}, claimer)
}

func assertErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, want, ec.Code)
}

type fakeDistributedNonceStore struct{}

func (fakeDistributedNonceStore) CheckAndMark(context.Context, string) error {
	return nil
}

func (fakeDistributedNonceStore) Kind() auth.NonceStoreKind {
	return auth.NonceStoreKindDistributed
}

type fakeDistributedClaimer struct{}

func (fakeDistributedClaimer) Claim(context.Context, string, time.Duration, time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimDone, nil, nil
}
