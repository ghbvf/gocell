package crypto_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validMasterKey is a 32-byte hex-encoded master key suitable for tests.
const validMasterKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// validMasterKeyPrevious is a different 32-byte hex master key.
const validMasterKeyPrevious = "2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestLocalAES(t *testing.T) *crypto.LocalAESKeyProvider {
	t.Helper()
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, "")
	require.NoError(t, err)
	return p
}

func newTestLocalAESWithPrevious(t *testing.T) *crypto.LocalAESKeyProvider {
	t.Helper()
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, validMasterKeyPrevious)
	require.NoError(t, err)
	return p
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_Current_ReturnsLatest
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_Current_ReturnsLatest(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	h, err := p.Current(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, h.ID())
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_ByID_OldKeyStillDecrypts
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_ByID_OldKeyStillDecrypts(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAESWithPrevious(t)

	// Get the current (latest) handle and encrypt something.
	current, err := p.Current(ctx)
	require.NoError(t, err)
	currentID := current.ID()

	plaintext := []byte("secret value")
	aad := []byte("cell:config-core/key:db_password")

	cipher, nonce, edk, err := current.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Retrieve the same key by ID and decrypt.
	handle, err := p.ByID(ctx, currentID)
	require.NoError(t, err)

	recovered, err := handle.Decrypt(ctx, cipher, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_ByID_PreviousKeyDecrypts
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_ByID_PreviousKeyDecrypts(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAESWithPrevious(t)

	// Encrypt with the previous key directly.
	prevHandle, err := p.ByID(ctx, crypto.LocalAESPreviousKeyID)
	require.NoError(t, err)

	plaintext := []byte("old secret")
	aad := []byte("cell:config-core/key:legacy_key")
	cipher, nonce, edk, err := prevHandle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Decrypt using the same previous handle.
	recovered, err := prevHandle.Decrypt(ctx, cipher, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_Rotate_AdvancesKeyID
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_Rotate_AdvancesKeyID(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	before, err := p.Current(ctx)
	require.NoError(t, err)
	oldID := before.ID()

	newID, err := p.Rotate(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, newID)
	assert.NotEqual(t, oldID, newID)

	after, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, newID, after.ID())
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_Rotate_OldKeyStillDecrypts
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_Rotate_OldKeyStillDecrypts(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	// Encrypt before rotation.
	before, err := p.Current(ctx)
	require.NoError(t, err)
	oldID := before.ID()

	plaintext := []byte("value before rotation")
	aad := []byte("cell:config-core/key:api_key")
	cipher, nonce, edk, err := before.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Rotate.
	_, err = p.Rotate(ctx)
	require.NoError(t, err)

	// Old key should still resolve.
	oldHandle, err := p.ByID(ctx, oldID)
	require.NoError(t, err)

	recovered, err := oldHandle.Decrypt(ctx, cipher, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_EnvelopeRoundTrip
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_EnvelopeRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	tests := []struct {
		name      string
		plaintext string
		aad       string
	}{
		{"empty plaintext", "", "cell:config-core/key:empty"},
		{"short value", "v", "cell:config-core/key:short"},
		{"long value", string(make([]byte, 4096)), "cell:config-core/key:long"},
		{"binary-like value", "\x00\x01\x02\xff\xfe", "cell:config-core/key:binary"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plaintext := []byte(tc.plaintext)
			aad := []byte(tc.aad)

			ct, nonce, edk, err := handle.Encrypt(ctx, plaintext, aad)
			require.NoError(t, err)
			// AES-GCM produces at least the 16-byte tag even for empty plaintext.
			assert.NotEmpty(t, ct)
			assert.NotEmpty(t, nonce)
			assert.NotEmpty(t, edk)

			recovered, err := handle.Decrypt(ctx, ct, nonce, edk, aad)
			require.NoError(t, err)
			// AES-GCM Open on empty plaintext returns nil; normalise both to empty.
			if len(recovered) == 0 {
				recovered = []byte{}
			}
			if len(plaintext) == 0 {
				plaintext = []byte{}
			}
			assert.Equal(t, plaintext, recovered)
		})
	}
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_MissingMasterKey_FailFast
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_MissingMasterKey_FailFast(t *testing.T) {
	_, err := crypto.NewLocalAESKeyProviderFromKeys("", "")
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrConfigKeyMissing, ec.Code)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_InvalidKey_FailFast
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_InvalidKey_FailFast(t *testing.T) {
	// Too short — not 32 bytes after decoding.
	_, err := crypto.NewLocalAESKeyProviderFromKeys("deadbeef", "")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_DecryptAADMismatch_FailClosed
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_DecryptAADMismatch_FailClosed(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	plaintext := []byte("sensitive value")
	aad := []byte("cell:config-core/key:my_key")
	wrongAAD := []byte("cell:config-core/key:other_key")

	cipher, nonce, edk, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	_, err = handle.Decrypt(ctx, cipher, nonce, edk, wrongAAD)
	require.Error(t, err, "mismatched AAD must cause decrypt failure")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_NonceUnique
// ---------------------------------------------------------------------------

func TestLocalAESKeyProvider_NonceUnique(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	plaintext := []byte("same value")
	aad := []byte("same aad")

	_, nonce1, _, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	_, nonce2, _, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	assert.NotEqual(t, nonce1, nonce2, "consecutive Encrypt calls must produce different nonces")
}
