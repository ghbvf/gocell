//go:build integration

package crypto_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
	vaultcontainer "github.com/testcontainers/testcontainers-go/modules/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startVaultContainer starts a Vault dev-mode container and returns the address,
// root token, and a teardown function. The transit secret engine is enabled
// and the key "gocell-config" is created during setup.
func startVaultContainer(t *testing.T) (addr, token string, teardown func()) {
	t.Helper()
	ctx := context.Background()

	container, err := vaultcontainer.Run(ctx,
		"hashicorp/vault:1.17",
		vaultcontainer.WithToken("root-test-token"),
		vaultcontainer.WithInitCommand(
			// Enable transit engine and create the test key.
			"secrets enable transit",
			"write -f transit/keys/gocell-config",
		),
	)
	if err != nil {
		t.Skipf("vault container unavailable: %v", err)
	}

	vaultAddr, err := container.HttpHostAddress(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("vault container address unavailable: %v", err)
	}

	teardown = func() {
		_ = container.Terminate(ctx)
	}
	return fmt.Sprintf("http://%s", vaultAddr), "root-test-token", teardown
}

// TestVaultTransitKeyProvider_Integration_RoundTrip verifies encrypt → decrypt
// round-trip using a real Vault dev container.
func TestVaultTransitKeyProvider_Integration_RoundTrip(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	t.Setenv("VAULT_ADDR", addr)
	t.Setenv("VAULT_TOKEN", token)
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", "gocell-config")

	p, err := crypto.NewVaultTransitKeyProviderFromEnv()
	require.NoError(t, err, "NewVaultTransitKeyProviderFromEnv should succeed with running vault")

	handle, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle.ID(), "vault-transit:v")

	plaintext := []byte("production-api-secret")
	aad := []byte("cell:config-core/key:api_key")

	ct, nonce, edk, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.NotEmpty(t, ct)
	assert.Nil(t, nonce, "VaultTransit does not use nonce")
	assert.Nil(t, edk, "VaultTransit does not use edk")

	// Round-trip: decrypt with matching AAD must return original plaintext.
	recovered, err := handle.Decrypt(ctx, ct, nil, nil, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// TestVaultTransitKeyProvider_Integration_AADMismatch_FailsClosed verifies that
// decrypting with a different AAD than used during encryption fails.
func TestVaultTransitKeyProvider_Integration_AADMismatch_FailsClosed(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	t.Setenv("VAULT_ADDR", addr)
	t.Setenv("VAULT_TOKEN", token)
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", "gocell-config")

	p, err := crypto.NewVaultTransitKeyProviderFromEnv()
	require.NoError(t, err)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	plaintext := []byte("secret-value")
	aad := []byte("cell:config-core/key:row_a")

	ct, _, _, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Decrypt with wrong AAD (cross-row replay attempt) must fail.
	wrongAAD := []byte("cell:config-core/key:row_b")
	_, err = handle.Decrypt(ctx, ct, nil, nil, wrongAAD)
	require.Error(t, err, "decrypt with mismatched AAD must fail-closed")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be errcode.Error, got: %v", err)
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// TestVaultTransitKeyProvider_Integration_Rotation verifies that after rotation:
//   - new writes use the new key version
//   - old ciphertext (encrypted with v1) is still decryptable
func TestVaultTransitKeyProvider_Integration_Rotation(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	t.Setenv("VAULT_ADDR", addr)
	t.Setenv("VAULT_TOKEN", token)
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", "gocell-config")

	p, err := crypto.NewVaultTransitKeyProviderFromEnv()
	require.NoError(t, err)

	// Encrypt with v1.
	handle1, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle1.ID(), "vault-transit:v1")

	plaintext := []byte("pre-rotation-value")
	aad := []byte("cell:config-core/key:old_key")

	ct1, _, _, err := handle1.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Rotate to v2.
	newID, err := p.Rotate(ctx)
	require.NoError(t, err)
	assert.Contains(t, newID, "vault-transit:v2")

	// New current key is v2.
	handle2, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle2.ID(), "vault-transit:v2")

	// Old ciphertext (v1) is still decryptable via ByID.
	handle1b, err := p.ByID(ctx, handle1.ID())
	require.NoError(t, err)
	recovered, err := handle1b.Decrypt(ctx, ct1, nil, nil, aad)
	require.NoError(t, err, "historical ciphertext must be decryptable after rotation")
	assert.Equal(t, plaintext, recovered)
}
