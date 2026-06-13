package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

const testServiceKey = "test-service-secret-at-least-32-bytes!!"

func TestInternalAuthChainMissingServiceSecretFailsFast(t *testing.T) {
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	_, err = newInternalAuthChain("", store)

	require.Error(t, err)
	require.Contains(t, err.Error(), ssobffServiceKeyEnv)
}

func TestInternalAuthChainContainsServiceToken(t *testing.T) {
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	chain, err := newInternalAuthChain(testServiceKey, store)

	require.NoError(t, err)
	require.NotEmpty(t, chain)
	require.True(t, authChainContainsServiceToken(chain))
}

func authChainContainsServiceToken(chain []kauth.ListenerAuth) bool {
	for _, plan := range chain {
		if _, ok := plan.(kauth.AuthServiceToken); ok {
			return true
		}
	}
	return false
}
