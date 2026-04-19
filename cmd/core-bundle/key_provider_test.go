package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildKeyProvider_MemoryMode_NoEnv_ReturnsNil verifies that in memory
// storage mode, an unset GOCELL_KEY_PROVIDER leads to nil (which the caller
// maps to NoopTransformer). No encryption is required for in-memory storage.
func TestBuildKeyProvider_MemoryMode_NoEnv_ReturnsNil(t *testing.T) {
	t.Setenv("GOCELL_KEY_PROVIDER", "")

	kp, err := buildKeyProvider("memory")
	require.NoError(t, err)
	assert.Nil(t, kp, "memory mode + empty env should return nil (no provider)")
}

// TestBuildKeyProvider_PostgresMode_NoEnv_FailsFast verifies that postgres
// storage mode without GOCELL_KEY_PROVIDER returns ErrConfigKeyMissing.
// This is the fail-fast guard that prevents silent NoopTransformer fallback,
// which would persist sensitive config values unencrypted (security invariant).
func TestBuildKeyProvider_PostgresMode_NoEnv_FailsFast(t *testing.T) {
	t.Setenv("GOCELL_KEY_PROVIDER", "")

	kp, err := buildKeyProvider("postgres")
	require.Error(t, err, "postgres mode without provider must fail-fast")
	assert.Nil(t, kp)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must be errcode.Error, got: %v", err)
	assert.Equal(t, errcode.ErrConfigKeyMissing, ecErr.Code,
		"postgres mode must fail with ErrConfigKeyMissing when GOCELL_KEY_PROVIDER is unset")
	assert.Contains(t, ecErr.Message, "GOCELL_KEY_PROVIDER must be set",
		"error message must name the env var so operators know what to set")
}

// TestBuildKeyProvider_UnknownProvider_Fails verifies that an unrecognised
// GOCELL_KEY_PROVIDER value fails fast rather than silently degrading.
func TestBuildKeyProvider_UnknownProvider_Fails(t *testing.T) {
	t.Setenv("GOCELL_KEY_PROVIDER", "bogus")

	kp, err := buildKeyProvider("postgres")
	require.Error(t, err)
	assert.Nil(t, kp)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "bogus")
	assert.Contains(t, ecErr.Message, "local-aes")
	assert.Contains(t, ecErr.Message, "vault-transit")
}

// TestBuildKeyProvider_LocalAES_Success verifies local-aes provider wiring.
func TestBuildKeyProvider_LocalAES_Success(t *testing.T) {
	// 32-byte hex-encoded master key.
	t.Setenv("GOCELL_KEY_PROVIDER", "local-aes")
	t.Setenv("GOCELL_MASTER_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	kp, err := buildKeyProvider("postgres")
	require.NoError(t, err)
	require.NotNil(t, kp)
}

// TestBuildKeyProvider_LocalAES_MissingKey_Fails verifies local-aes fails
// when GOCELL_MASTER_KEY is absent.
func TestBuildKeyProvider_LocalAES_MissingKey_Fails(t *testing.T) {
	t.Setenv("GOCELL_KEY_PROVIDER", "local-aes")
	t.Setenv("GOCELL_MASTER_KEY", "")

	kp, err := buildKeyProvider("postgres")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "local-aes")
}

// TestKeyProviderToTransformer_NilReturnsNoop verifies that a nil provider
// falls through to NoopTransformer (used for memory-mode tests that do not
// encrypt).
func TestKeyProviderToTransformer_NilReturnsNoop(t *testing.T) {
	vt := keyProviderToTransformer(nil)
	require.NotNil(t, vt)
	// Ensure it's actually a no-op: encrypt returns plaintext unchanged.
	ct, keyID, nonce, edk, err := vt.Encrypt(context.Background(), []byte("plain"), nil)
	require.NoError(t, err)
	assert.Equal(t, "plain", string(ct))
	assert.Empty(t, keyID)
	assert.Nil(t, nonce)
	assert.Nil(t, edk)
}
