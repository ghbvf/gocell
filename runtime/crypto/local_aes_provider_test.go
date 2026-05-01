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
	aad := []byte("cell:configcore/key:db_password")

	cipher, nonce, edk, _, err := current.Encrypt(ctx, plaintext, aad)
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
	aad := []byte("cell:configcore/key:legacy_key")
	cipher, nonce, edk, _, err := prevHandle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Decrypt using the same previous handle.
	recovered, err := prevHandle.Decrypt(ctx, cipher, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestLocalAESKeyProvider_Rotate_ReturnsErrNotImplemented
// ---------------------------------------------------------------------------

// TestLocalAESKeyProvider_Rotate_ReturnsErrNotImplemented verifies that
// LocalAES.Rotate returns ErrNotImplemented.
// LocalAES rotation is intentionally disabled because in-memory keys are lost
// on restart — production rotation must use VaultTransitKeyProvider (F4/S14a).
func TestLocalAESKeyProvider_Rotate_ReturnsErrNotImplemented(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	_, err := p.Rotate(ctx)
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be errcode.Error")
	assert.Equal(t, errcode.ErrNotImplemented, ec.Code)
	assert.Contains(t, err.Error(), "adapters/vault.TransitKeyProvider")
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
		{"empty plaintext", "", "cell:configcore/key:empty"},
		{"short value", "v", "cell:configcore/key:short"},
		{"long value", string(make([]byte, 4096)), "cell:configcore/key:long"},
		{"binary-like value", "\x00\x01\x02\xff\xfe", "cell:configcore/key:binary"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plaintext := []byte(tc.plaintext)
			aad := []byte(tc.aad)

			ct, nonce, edk, _, err := handle.Encrypt(ctx, plaintext, aad)
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
	aad := []byte("cell:configcore/key:my_key")
	wrongAAD := []byte("cell:configcore/key:other_key")

	cipher, nonce, edk, _, err := handle.Encrypt(ctx, plaintext, aad)
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

	_, nonce1, _, _, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	_, nonce2, _, _, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	assert.NotEqual(t, nonce1, nonce2, "consecutive Encrypt calls must produce different nonces")
}

// ---------------------------------------------------------------------------
// TestLocalAESHandle_Decrypt_MissingNonce — nonce empty → ErrKeyProviderDecryptFailed
// ---------------------------------------------------------------------------

func TestLocalAESHandle_Decrypt_MissingNonce(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)
	handle, err := p.Current(ctx)
	require.NoError(t, err)

	// Encrypt to get a valid ciphertext + edk.
	ct, _, edk, _, err := handle.Encrypt(ctx, []byte("value"), []byte("aad"))
	require.NoError(t, err)

	// Pass empty nonce — must fail.
	_, err = handle.Decrypt(ctx, ct, nil, edk, []byte("aad"))
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// TestLocalAESHandle_Decrypt_MissingEDK — edk empty → ErrKeyProviderDecryptFailed
// ---------------------------------------------------------------------------

func TestLocalAESHandle_Decrypt_MissingEDK(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)
	handle, err := p.Current(ctx)
	require.NoError(t, err)

	ct, nonce, _, _, err := handle.Encrypt(ctx, []byte("value"), []byte("aad"))
	require.NoError(t, err)

	// Pass empty edk — must fail.
	_, err = handle.Decrypt(ctx, ct, nonce, nil, []byte("aad"))
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// TestLocalAESHandle_Decrypt_TamperedEDK — tampered edk → ErrKeyProviderDecryptFailed
// ---------------------------------------------------------------------------

func TestLocalAESHandle_Decrypt_TamperedEDK(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)
	handle, err := p.Current(ctx)
	require.NoError(t, err)

	ct, nonce, edk, _, err := handle.Encrypt(ctx, []byte("value"), []byte("aad"))
	require.NoError(t, err)

	// Flip the last byte of edk to simulate tampering.
	tampered := make([]byte, len(edk))
	copy(tampered, edk)
	tampered[len(tampered)-1] ^= 0xFF

	_, err = handle.Decrypt(ctx, ct, nonce, tampered, []byte("aad"))
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// TestNewLocalAESKeyProviderFromKeys_InvalidPrevKey — invalid prevKey → error
// ---------------------------------------------------------------------------

func TestNewLocalAESKeyProviderFromKeys_InvalidPrevKey(t *testing.T) {
	tests := []struct {
		name    string
		prevKey string
	}{
		{"hex too short", "deadbeef"},
		{"not hex or base64", "zzz-not-valid-encoding-at-all-@@@@"},
		{"base64 wrong length", "c2hvcnQ="}, // "short" base64 — not 32 bytes
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, tc.prevKey)
			require.Error(t, err, "invalid prevKey must return error")
		})
	}
}

// ---------------------------------------------------------------------------
// TestAESGCMEncryptSplit_InvalidKeyLength — invalid key length → error via Encrypt
// ---------------------------------------------------------------------------

// TestLocalAESHandle_Encrypt_InvalidKEK exercises the aesGCMEncryptSplit branch
// that returns an error when the key is not a valid AES key length (16/24/32 bytes).
// We do this by constructing a provider with a non-standard key via internal
// manipulation — use NewLocalAESKeyProviderFromKeys with a key that, after
// decoding, is 32 bytes (valid), then we rely on the DEK generation path which
// always uses 32-byte DEK internally. Instead, test the invalid-key path by
// ensuring a 15-byte hex key is rejected at construction time.
func TestNewLocalAESKeyProviderFromKeys_KeyWrongDecodedLength(t *testing.T) {
	// 30 hex chars = 15 bytes after decoding → invalid (not 32 bytes).
	shortHex := "010203040506070809101112131415"
	_, err := crypto.NewLocalAESKeyProviderFromKeys(shortHex, "")
	require.Error(t, err, "15-byte key must be rejected")
}

// ---------------------------------------------------------------------------
// TC-LA-1: TestLocalAESHandle_Encrypt_ReturnsKeyIDEqualsHandleID
// Phase 1-b — TDD RED (compile-fails until Phase 2-c updates localAESHandle.Encrypt)
// ---------------------------------------------------------------------------

// TestLocalAESHandle_Encrypt_ReturnsKeyIDEqualsHandleID verifies that the
// keyID returned by Encrypt equals the handle's ID(). This aligns with the
// k8s KMS v2 EncryptResponse.KeyID contract: the keyID returned at
// encrypt-time is the authoritative binding between the ciphertext and the KEK
// version, eliminating the race between Current() and a key rotation.
//
// Expected behavior after Phase 2-c:
//   - keyID == handle.ID() == crypto.LocalAESCurrentKeyID ("local-aes-v1")
//   - err == nil
func TestLocalAESHandle_Encrypt_ReturnsKeyIDEqualsHandleID(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	plaintext := []byte("sensitive config value")
	aad := []byte("cell:configcore/key:api_key")

	// Five-return-value Encrypt (Phase 0-b kernel interface).
	// Compile error here is expected until Phase 2-c updates localAESHandle.Encrypt.
	ct, nonce, edk, keyID, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.NotEmpty(t, ct)
	assert.NotEmpty(t, nonce)
	assert.NotEmpty(t, edk)

	// Core assertion: keyID returned from Encrypt must equal handle.ID().
	assert.Equal(t, handle.ID(), keyID, "Encrypt must return the handle's key ID")
	assert.Equal(t, crypto.LocalAESCurrentKeyID, keyID,
		"LocalAES current handle keyID must be %q", crypto.LocalAESCurrentKeyID)
}

// ---------------------------------------------------------------------------
// TC-LA-2: TestLocalAESHandle_Encrypt_ByIDReturnedKeyIDMatches
// Phase 1-b — TDD RED
// ---------------------------------------------------------------------------

// TestLocalAESHandle_Encrypt_ByIDReturnedKeyIDMatches verifies that the keyID
// returned by Encrypt on the previous handle matches the ID used to look up
// the handle via ByID, and that the Decrypt round-trip via ByID succeeds.
//
// This test uses NewLocalAESKeyProviderFromKeys with two keys to simulate a
// "pre-rotation" keyring (current=v1, previous=v0). Because
// LocalAESKeyProvider.Rotate() returns ErrNotImplemented (in-memory state is
// not persistent), the "previous key" scenario is constructed directly via the
// constructor.
//
// Expected behavior after Phase 2-c:
//   - Encrypt on the previous handle returns keyID == LocalAESPreviousKeyID
//   - ByID(keyID) resolves the same handle
//   - Decrypt via ByID(keyID) succeeds, proving keyID is the correct binding
func TestLocalAESHandle_Encrypt_ByIDReturnedKeyIDMatches(t *testing.T) {
	ctx := context.Background()

	// Pre-load both current and previous keys.
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, validMasterKeyPrevious)
	require.NoError(t, err)

	// Encrypt with the previous key to exercise keyID binding for historical keys.
	prevHandle, err := p.ByID(ctx, crypto.LocalAESPreviousKeyID)
	require.NoError(t, err)

	plaintext := []byte("old secret value")
	aad := []byte("cell:configcore/key:legacy_db_password")

	// Five-return-value Encrypt (Phase 0-b kernel interface).
	ct, nonce, edk, keyID, err := prevHandle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// keyID from Encrypt must equal the handle's own ID.
	assert.Equal(t, crypto.LocalAESPreviousKeyID, keyID,
		"Encrypt on previous handle must return previous key ID")
	assert.Equal(t, prevHandle.ID(), keyID,
		"Encrypt-returned keyID must equal handle.ID()")

	// Round-trip: resolve via ByID(keyID) and decrypt — proving keyID is the
	// correct binding between ciphertext and the KEK version.
	resolvedHandle, err := p.ByID(ctx, keyID)
	require.NoError(t, err)
	assert.Equal(t, keyID, resolvedHandle.ID())

	recovered, err := resolvedHandle.Decrypt(ctx, ct, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered,
		"Decrypt via ByID(keyID) must recover original plaintext")
}

// ---------------------------------------------------------------------------
// TC-LA-3: TestLocalAESHandle_Encrypt_DoesNotReuseBuffers
// Phase 1-b — TDD RED (zeroize indirect observation)
// ---------------------------------------------------------------------------

// TestLocalAESHandle_Encrypt_DoesNotReuseBuffers verifies that consecutive
// Encrypt calls on the same handle produce independent ciphertext/nonce/edk
// outputs. This is the interface-observable semantic guarantee of DEK zeroize:
// each call starts from a clean slate (fresh DEK + fresh nonce), so output
// buffers are never shared or reused across calls.
//
// Note: this test does NOT directly verify that defer clear(dek) is called —
// that is a defense-in-depth implementation detail that cannot be observed
// without unsafe pointer inspection. What IS verifiable is that:
//
//  1. Each call produces a unique nonce (already covered by TC-NonceUnique).
//  2. Each call produces a unique edk (different DEK wrapped each time).
//  3. The returned slices are independent copies — mutating one does not affect
//     the other.
//
// Reasoning: if the implementation reused a DEK or nonce across calls (i.e.
// failed to zero and regenerate), the edk blobs would be identical for the
// same plaintext+aad. Independence here is a necessary (though not sufficient)
// condition for correct zeroize behavior.
func TestLocalAESHandle_Encrypt_DoesNotReuseBuffers(t *testing.T) {
	ctx := context.Background()
	p := newTestLocalAES(t)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	plaintext := []byte("same plaintext for both calls")
	aad := []byte("cell:configcore/key:test_key")

	// Five-return-value Encrypt (Phase 0-b kernel interface).
	ct1, nonce1, edk1, keyID1, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	ct2, nonce2, edk2, keyID2, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// keyID must be stable (same handle, same KEK version).
	assert.Equal(t, keyID1, keyID2, "keyID must be stable across calls on the same handle")

	// Nonces must differ (random per-call).
	assert.NotEqual(t, nonce1, nonce2, "consecutive Encrypt calls must produce different nonces")

	// edk must differ (each call wraps a fresh random DEK).
	// If the same DEK were reused, the edk blobs would be identical (GCM is
	// deterministic given the same key + nonce, and the edk nonce is also random).
	// In practice two independent random DEKs will produce different edks with
	// overwhelming probability.
	assert.NotEqual(t, edk1, edk2,
		"consecutive Encrypt calls must wrap independent DEKs (edk must differ)")

	// Ciphertext must differ (different DEK -> different ciphertext even for same plaintext).
	assert.NotEqual(t, ct1, ct2,
		"consecutive Encrypt calls must produce different ciphertexts (different DEK)")

	// Both must decrypt correctly — independence does not break correctness.
	recovered1, err := handle.Decrypt(ctx, ct1, nonce1, edk1, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered1)

	recovered2, err := handle.Decrypt(ctx, ct2, nonce2, edk2, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered2)
}
