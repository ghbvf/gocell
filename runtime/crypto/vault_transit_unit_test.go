package crypto_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fake vault client for unit tests
// ---------------------------------------------------------------------------

// fakeVaultClient simulates a minimal Vault Transit server in memory.
type fakeVaultClient struct {
	version int
	broken  bool // simulate network failure
	// stored as map[ciphertext string]plaintext bytes
	store map[string][]byte
}

func newFakeVaultClient() *fakeVaultClient {
	return &fakeVaultClient{version: 1, store: make(map[string][]byte)}
}

func (c *fakeVaultClient) Write(ctx context.Context, path string, data map[string]any) (map[string]any, error) {
	if c.broken {
		return nil, errors.New("vault: connection refused")
	}
	switch {
	case isEncryptPath(path):
		encoded, _ := data["plaintext"].(string)
		raw, _ := base64.StdEncoding.DecodeString(encoded)
		ct := fakeCiphertext(raw, c.version)
		c.store[ct] = raw
		return map[string]any{"ciphertext": ct}, nil

	case isDecryptPath(path):
		ct, _ := data["ciphertext"].(string)
		pt, ok := c.store[ct]
		if !ok {
			return nil, errors.New("vault: ciphertext not found")
		}
		return map[string]any{
			"plaintext": base64.StdEncoding.EncodeToString(pt),
		}, nil

	case isRotatePath(path):
		c.version++
		return map[string]any{}, nil
	}
	return nil, errors.New("vault: unknown path: " + path)
}

func (c *fakeVaultClient) Read(_ context.Context, _ string) (map[string]any, error) {
	if c.broken {
		return nil, errors.New("vault: connection refused")
	}
	return map[string]any{"latest_version": float64(c.version)}, nil
}

// isEncryptPath returns true when path contains "/encrypt/".
func isEncryptPath(p string) bool {
	return len(p) > 8 && p[len(p)-len("/encrypt/gocell-config"):] == "/encrypt/gocell-config"
}

// isDecryptPath returns true when path contains "/decrypt/".
func isDecryptPath(p string) bool {
	return len(p) > 8 && p[len(p)-len("/decrypt/gocell-config"):] == "/decrypt/gocell-config"
}

// isRotatePath returns true when path ends with "/rotate".
func isRotatePath(p string) bool {
	return len(p) >= 7 && p[len(p)-7:] == "/rotate"
}

// fakeCiphertext produces a deterministic fake ciphertext string.
func fakeCiphertext(plaintext []byte, version int) string {
	return "vault:v" + itoa(version) + ":" + base64.StdEncoding.EncodeToString(plaintext)
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return "N"
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestVaultTransitKeyProvider_EncryptDecrypt_RoundTrip(t *testing.T) {
	ctx := context.Background()
	client := newFakeVaultClient()
	p := crypto.NewVaultTransitKeyProvider(client, "transit", "gocell-config")

	handle, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, "vault-transit:v1", handle.ID())

	plaintext := []byte("sensitive-api-key")
	aad := []byte("cell:config-core/key:api_key")

	ct, nonce, edk, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.NotEmpty(t, ct)
	// VaultTransit does not use nonce/edk.
	assert.Nil(t, nonce)
	assert.Nil(t, edk)

	recovered, err := handle.Decrypt(ctx, ct, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

func TestVaultTransitKeyProvider_Rotate_AdvancesVersion(t *testing.T) {
	ctx := context.Background()
	client := newFakeVaultClient()
	p := crypto.NewVaultTransitKeyProvider(client, "transit", "gocell-config")

	newID, err := p.Rotate(ctx)
	require.NoError(t, err)
	assert.Equal(t, "vault-transit:v2", newID)

	current, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, "vault-transit:v2", current.ID())
}

func TestVaultTransitKeyProvider_ByID_Returns_Handle(t *testing.T) {
	ctx := context.Background()
	client := newFakeVaultClient()
	p := crypto.NewVaultTransitKeyProvider(client, "transit", "gocell-config")

	h, err := p.ByID(ctx, "vault-transit:v1")
	require.NoError(t, err)
	assert.Equal(t, "vault-transit:v1", h.ID())
}

func TestVaultTransitKeyProvider_ByID_InvalidPrefix_Fails(t *testing.T) {
	ctx := context.Background()
	client := newFakeVaultClient()
	p := crypto.NewVaultTransitKeyProvider(client, "transit", "gocell-config")

	_, err := p.ByID(ctx, "local-aes-v1")
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderKeyNotFound, ec.Code)
}

func TestVaultTransitKeyProvider_NetworkFailure_FailClosed(t *testing.T) {
	ctx := context.Background()
	client := newFakeVaultClient()
	p := crypto.NewVaultTransitKeyProvider(client, "transit", "gocell-config")

	// Encrypt while healthy.
	handle, err := p.Current(ctx)
	require.NoError(t, err)
	ct, _, _, err := handle.Encrypt(ctx, []byte("secret"), nil)
	require.NoError(t, err)

	// Break the client to simulate vault going down.
	client.broken = true

	// Decrypt must fail-closed.
	_, err = handle.Decrypt(ctx, ct, nil, nil, nil)
	require.Error(t, err, "decrypt with broken vault must return an error")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}
