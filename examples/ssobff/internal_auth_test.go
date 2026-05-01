package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
)

const testServiceKey = "test-service-secret-at-least-32-bytes!!"

func TestInternalAuthChainMissingServiceSecretFailsFast(t *testing.T) {
	t.Setenv(ssobffServiceKeyEnv, "")

	_, err := newInternalAuthChainFromEnv()

	require.Error(t, err)
	require.Contains(t, err.Error(), ssobffServiceKeyEnv)
}

func TestInternalAuthChainContainsServiceToken(t *testing.T) {
	t.Setenv(ssobffServiceKeyEnv, testServiceKey)

	chain, err := newInternalAuthChainFromEnv()

	require.NoError(t, err)
	require.NotEmpty(t, chain)
	require.True(t, authChainContainsServiceToken(chain))
}

func authChainContainsServiceToken(chain []cell.ListenerAuth) bool {
	for _, plan := range chain {
		if _, ok := plan.(cell.AuthServiceToken); ok {
			return true
		}
	}
	return false
}
