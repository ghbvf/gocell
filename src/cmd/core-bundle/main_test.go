package main

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadKeySet_DevMode(t *testing.T) {
	ks, err := loadKeySet("")
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadKeySet_RealMode_MissingEnv(t *testing.T) {
	// Real mode without env vars should fail-fast.
	t.Setenv(auth.EnvJWTPrivateKey, "")
	t.Setenv(auth.EnvJWTPublicKey, "")

	_, err := loadKeySet("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), auth.EnvJWTPrivateKey)
}

func TestEnvOrDefault_WithEnv(t *testing.T) {
	t.Setenv("TEST_KEY_FOR_ENVDEFAULT", "actual-value")
	got := envOrDefault("TEST_KEY_FOR_ENVDEFAULT", "fallback")
	assert.Equal(t, []byte("actual-value"), got)
}

func TestEnvOrDefault_Fallback(t *testing.T) {
	t.Setenv("TEST_KEY_FOR_ENVDEFAULT_MISS", "")
	got := envOrDefault("TEST_KEY_FOR_ENVDEFAULT_MISS", "fallback")
	assert.Equal(t, []byte("fallback"), got)
}
