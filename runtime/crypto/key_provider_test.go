package crypto_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fake KeyHandle for interface-contract tests
// ---------------------------------------------------------------------------

// fakeKeyHandle is a simple in-memory XOR cipher — just enough to verify
// that the interface contract (AAD binding, nonce uniqueness) holds.
type fakeKeyHandle struct {
	id      string
	counter int
}

func (h *fakeKeyHandle) ID() string { return h.id }

// Encrypt XORs plaintext with 0x42 (dummy cipher), produces a unique nonce
// via counter, and binds aad by prepending it to the "ciphertext" for
// round-trip verification.
func (h *fakeKeyHandle) Encrypt(_ context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, err error) {
	h.counter++
	// nonce = 12-byte counter value
	nonce = make([]byte, 12)
	nonce[11] = byte(h.counter)
	// edk = key id bytes (stand-in for wrapped DEK)
	edk = []byte(h.id)
	// ciphertext = len(aad) || aad || XOR(plaintext)
	ct := make([]byte, 1+len(aad)+len(plaintext))
	ct[0] = byte(len(aad))
	copy(ct[1:], aad)
	for i, b := range plaintext {
		ct[1+len(aad)+i] = b ^ 0x42
	}
	return ct, nonce, edk, nil
}

// Decrypt reverses fakeKeyHandle.Encrypt; returns ErrDecryptFailed if the
// embedded AAD does not match.
func (h *fakeKeyHandle) Decrypt(_ context.Context, ciphertext, _, _, aad []byte) (plaintext []byte, err error) {
	if len(ciphertext) < 1 {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "ciphertext too short")
	}
	aadLen := int(ciphertext[0])
	if len(ciphertext) < 1+aadLen {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "ciphertext truncated")
	}
	storedAAD := ciphertext[1 : 1+aadLen]
	if !bytes.Equal(storedAAD, aad) {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "AAD mismatch")
	}
	ct := ciphertext[1+aadLen:]
	pt := make([]byte, len(ct))
	for i, b := range ct {
		pt[i] = b ^ 0x42
	}
	return pt, nil
}

// ---------------------------------------------------------------------------
// Fake KeyProvider
// ---------------------------------------------------------------------------

type fakeKeyProvider struct {
	keys    map[string]*fakeKeyHandle
	current string
}

func newFakeKeyProvider(initialID string) *fakeKeyProvider {
	p := &fakeKeyProvider{
		keys:    make(map[string]*fakeKeyHandle),
		current: initialID,
	}
	p.keys[initialID] = &fakeKeyHandle{id: initialID}
	return p
}

func (p *fakeKeyProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	h, ok := p.keys[p.current]
	if !ok {
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound, "current key not found")
	}
	return h, nil
}

func (p *fakeKeyProvider) ByID(_ context.Context, keyID string) (crypto.KeyHandle, error) {
	h, ok := p.keys[keyID]
	if !ok {
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound, "key not found: "+keyID)
	}
	return h, nil
}

func (p *fakeKeyProvider) Rotate(_ context.Context) (string, error) {
	newID := p.current + "-v2"
	p.keys[newID] = &fakeKeyHandle{id: newID}
	p.current = newID
	return newID, nil
}

// ---------------------------------------------------------------------------
// TestKeyProvider_Current_ReturnsLatest
// ---------------------------------------------------------------------------

func TestKeyProvider_Current_ReturnsLatest(t *testing.T) {
	ctx := context.Background()
	p := newFakeKeyProvider("key-v1")

	h, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, "key-v1", h.ID())
}

// ---------------------------------------------------------------------------
// TestKeyProvider_ByID_ResolvesHistorical
// ---------------------------------------------------------------------------

func TestKeyProvider_ByID_ResolvesHistorical(t *testing.T) {
	ctx := context.Background()
	p := newFakeKeyProvider("key-v1")

	// Rotate to v2 — v1 should still resolve.
	_, err := p.Rotate(ctx)
	require.NoError(t, err)

	old, err := p.ByID(ctx, "key-v1")
	require.NoError(t, err)
	assert.Equal(t, "key-v1", old.ID())
}

// ---------------------------------------------------------------------------
// TestKeyProvider_ByID_NotFound
// ---------------------------------------------------------------------------

func TestKeyProvider_ByID_NotFound(t *testing.T) {
	ctx := context.Background()
	p := newFakeKeyProvider("key-v1")

	_, err := p.ByID(ctx, "nonexistent")
	require.Error(t, err)

	var errcode_ *errcode.Error
	require.True(t, errors.As(err, &errcode_))
	assert.Equal(t, errcode.ErrKeyProviderKeyNotFound, errcode_.Code)
}

// ---------------------------------------------------------------------------
// TestKeyProvider_Rotate_AdvancesKeyID
// ---------------------------------------------------------------------------

func TestKeyProvider_Rotate_AdvancesKeyID(t *testing.T) {
	ctx := context.Background()
	p := newFakeKeyProvider("key-v1")

	newID, err := p.Rotate(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, newID)
	assert.NotEqual(t, "key-v1", newID)

	h, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, newID, h.ID())
}

// ---------------------------------------------------------------------------
// TestKeyHandle_EncryptDecrypt_AADConsistent
// ---------------------------------------------------------------------------

func TestKeyHandle_EncryptDecrypt_AADConsistent(t *testing.T) {
	ctx := context.Background()
	h := &fakeKeyHandle{id: "test-key"}
	plaintext := []byte("super-secret-value")
	aad := []byte("cell:config-core/key:api_key")

	// Round-trip with matching AAD should succeed.
	cipher, nonce, edk, err := h.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	recovered, err := h.Decrypt(ctx, cipher, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)

	// Different AAD must fail.
	_, err = h.Decrypt(ctx, cipher, nonce, edk, []byte("cell:config-core/key:other_key"))
	require.Error(t, err, "different AAD should produce a decrypt error")
}

// ---------------------------------------------------------------------------
// TestKeyHandle_EncryptDecrypt_NonceUnique
// ---------------------------------------------------------------------------

func TestKeyHandle_EncryptDecrypt_NonceUnique(t *testing.T) {
	ctx := context.Background()
	h := &fakeKeyHandle{id: "test-key"}
	plaintext := []byte("value")
	aad := []byte("aad")

	_, nonce1, _, err := h.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	_, nonce2, _, err := h.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	assert.NotEqual(t, nonce1, nonce2, "consecutive Encrypt calls must produce different nonces")
}

// ---------------------------------------------------------------------------
// TestKeyHandle_Decrypt_FailClosedOnWrongCiphertext
// ---------------------------------------------------------------------------

func TestKeyHandle_Decrypt_FailClosedOnWrongCiphertext(t *testing.T) {
	ctx := context.Background()
	h := &fakeKeyHandle{id: "test-key"}

	// Empty ciphertext.
	_, err := h.Decrypt(ctx, []byte{}, nil, nil, []byte("aad"))
	require.Error(t, err)
}
