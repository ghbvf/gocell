package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validMasterKeyHex is a non-demo 32-byte master key in hex for tests.
const validMasterKeyHex = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

// demoMasterKeyHex is the well-known demo key shipped in test fixtures.
const demoMasterKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestBuildKeyProvider_MemoryMode_NoEnv_ReturnsNoKey verifies that in memory
// storage mode, an empty providerName leads to the no-key sentinel (which the
// caller maps to NoopTransformer). No encryption is required for in-memory storage.
func TestBuildKeyProvider_MemoryMode_NoEnv_ReturnsNoKey(t *testing.T) {
	kp, err := buildKeyProvider("memory", "", "", "", "")
	require.NoError(t, err)
	assert.True(t, isNoKeyProvider(kp), "memory mode + empty provider should return no-key sentinel")
}

// TestBuildKeyProvider_PostgresMode_NoEnv_FailsFast verifies that postgres
// storage mode with an empty providerName returns ErrConfigKeyMissing.
// This is the fail-fast guard that prevents silent NoopTransformer fallback,
// which would persist sensitive config values unencrypted (security invariant).
func TestBuildKeyProvider_PostgresMode_NoEnv_FailsFast(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "", "", "", "")
	require.Error(t, err, "postgres mode without provider must fail-fast")
	assert.Nil(t, kp)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must be errcode.Error, got: %v", err)
	assert.Equal(t, errcode.ErrConfigKeyMissing, ecErr.Code,
		"postgres mode must fail with ErrConfigKeyMissing when GOCELL_CONFIGCORE_KEY_PROVIDER is unset")
	assert.Contains(t, ecErr.Message, "GOCELL_CONFIGCORE_KEY_PROVIDER must be set",
		"error message must name the env var so operators know what to set")
}

// TestBuildKeyProvider_UnknownProvider_Fails verifies that an unrecognized
// providerName fails fast rather than silently degrading.
func TestBuildKeyProvider_UnknownProvider_Fails(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "", "bogus", "", "")
	require.Error(t, err)
	assert.Nil(t, kp)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "bogus")
	assert.Contains(t, ecErr.Message, "local-aes")
	assert.Contains(t, ecErr.Message, "vault-transit")
}

// TestBuildKeyProvider_LocalAES_Success verifies local-aes provider wiring
// with a non-demo key in dev mode.
func TestBuildKeyProvider_LocalAES_Success(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "dev", "local-aes", validMasterKeyHex, "")
	require.NoError(t, err)
	require.NotNil(t, kp)
}

// TestBuildKeyProvider_LocalAES_DemoKey_RealMode_Rejected verifies that
// local-aes with a well-known demo master key is rejected in real mode.
func TestBuildKeyProvider_LocalAES_DemoKey_RealMode_Rejected(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "real", "local-aes", demoMasterKeyHex, "")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "well-known demo key")
}

// TestBuildKeyProvider_LocalAES_DemoKey_DevMode_Allowed verifies demo key is
// accepted in dev mode.
func TestBuildKeyProvider_LocalAES_DemoKey_DevMode_Allowed(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "dev", "local-aes", demoMasterKeyHex, "")
	require.NoError(t, err)
	require.NotNil(t, kp)
}

// TestBuildKeyProvider_LocalAES_MissingKey_Fails verifies local-aes fails
// when master key is absent.
func TestBuildKeyProvider_LocalAES_MissingKey_Fails(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "", "local-aes", "", "")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "local-aes")
}

// TestBuildKeyProvider_VaultTransit_InvalidAddr_FailsFast verifies the
// vault-transit wiring path in buildKeyProvider: the provider construction
// must surface a startup error when VAULT_ADDR points at an unreachable
// endpoint, rather than silently degrading to NoopTransformer.
//
// Success-case wiring is covered by adapters/vault integration tests (real
// Vault container); this unit test only locks the cmd/corebundle wiring
// so buildKeyProvider's vault-transit branch does not regress silently.
func TestBuildKeyProvider_VaultTransit_InvalidAddr_FailsFast(t *testing.T) {
	// Deliberately unreachable address — readLatestVersion must surface the
	// connection failure as a startup error (transient, but still fatal at
	// startup since the fail-fast check runs in NewTransitKeyProviderFromEnv).
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1")
	t.Setenv("VAULT_AUTH_METHOD", "token")
	t.Setenv("VAULT_TOKEN", "test-token")

	kp, err := buildKeyProvider("postgres", "dev", "vault-transit", "", "")
	require.Error(t, err, "vault-transit with unreachable VAULT_ADDR must fail startup")
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "vault-transit",
		"error must identify provider so operators can route the alert")
	// Confirm we actually reached the readLatestVersion / network-error path,
	// not the earlier env-validation path.
	assert.NotContains(t, err.Error(), "VAULT_AUTH_METHOD is required",
		"test should exercise the unreachable-VAULT_ADDR path, not env validation")
}

// TestBuildKeyProvider_LocalAES_DemoKey_UpperCase_RealMode_Rejected verifies
// that an uppercase-hex variant of a well-known demo key is rejected in real
// mode. hex.DecodeString is case-insensitive, so "0123ABCD..." and "0123abcd..."
// produce identical key material; the demo-key check must normalize to lowercase
// to catch both forms.
func TestBuildKeyProvider_LocalAES_DemoKey_UpperCase_RealMode_Rejected(t *testing.T) {
	const upperDemoKey = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	kp, err := buildKeyProvider("postgres", "real", "local-aes", upperDemoKey, "")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "well-known demo key")
}

// TestBuildKeyProvider_LocalAES_DemoKey_MixedCase_RealMode_Rejected verifies
// that a mixed-case variant of a well-known demo key is rejected in real mode.
func TestBuildKeyProvider_LocalAES_DemoKey_MixedCase_RealMode_Rejected(t *testing.T) {
	const mixedDemoKey = "0123456789AbCdEf0123456789AbCdEf0123456789AbCdEf0123456789AbCdEf"
	kp, err := buildKeyProvider("postgres", "real", "local-aes", mixedDemoKey, "")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "well-known demo key")
}

// TestBuildKeyProvider_PrevMasterKeyDemo_FailsFast verifies that a well-known
// demo key used as prevMasterKey is rejected in real adapter mode. Previously
// only the primary masterKey was checked, leaving the rotation key as an
// active decryption path without demo-key validation (F2 fix).
func TestBuildKeyProvider_PrevMasterKeyDemo_FailsFast(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "real", "local-aes", validMasterKeyHex, demoMasterKeyHex)
	require.Error(t, err, "real mode must reject demo prevMasterKey")
	assert.Nil(t, kp)
	assert.Contains(t, err.Error(), "GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS",
		"error must name the env var so operators can identify which key to rotate")
	assert.Contains(t, err.Error(), "well-known demo key")
}

// TestBuildKeyProvider_PrevMasterKeyDemo_DevMode_Allowed verifies that a demo
// prevMasterKey is accepted in non-real adapter modes (dev/CI).
func TestBuildKeyProvider_PrevMasterKeyDemo_DevMode_Allowed(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "dev", "local-aes", validMasterKeyHex, demoMasterKeyHex)
	require.NoError(t, err)
	require.NotNil(t, kp)
}

// TestBuildKeyProvider_PrevMasterKeyEmpty_RealMode_OK verifies that an empty
// prevMasterKey is accepted in real mode (key rotation not configured).
func TestBuildKeyProvider_PrevMasterKeyEmpty_RealMode_OK(t *testing.T) {
	kp, err := buildKeyProvider("postgres", "real", "local-aes", validMasterKeyHex, "")
	require.NoError(t, err)
	require.NotNil(t, kp)
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
