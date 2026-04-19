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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestTransformer(t *testing.T) crypto.ValueTransformer {
	t.Helper()
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, "")
	require.NoError(t, err)
	return crypto.NewValueTransformer(p)
}

// ---------------------------------------------------------------------------
// TestValueTransformer_EncryptDecrypt_RoundTrip
// ---------------------------------------------------------------------------

func TestValueTransformer_EncryptDecrypt_RoundTrip(t *testing.T) {
	ctx := context.Background()
	tr := newTestTransformer(t)

	tests := []struct {
		name      string
		value     string
		configKey string
	}{
		{"api key", "sk-secretapikey1234", "api_key"},
		{"database password", "p@ssw0rd!", "db_password"},
		{"empty value", "", "empty_config"},
		{"unicode value", "秘密の値", "unicode_key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plaintext := []byte(tc.value)
			aad := crypto.AADForConfig("config-core", tc.configKey)

			ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
			require.NoError(t, err)
			assert.NotEmpty(t, keyID)
			assert.NotEmpty(t, nonce)
			assert.NotEmpty(t, edk)

			recovered, err := tr.Decrypt(ctx, ct, keyID, nonce, edk, aad)
			require.NoError(t, err)
			// AES-GCM Open on empty plaintext returns nil; normalise both.
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
// TestValueTransformer_DecryptByHistoricalKeyID
// ---------------------------------------------------------------------------

// TestValueTransformer_DecryptByHistoricalKeyID verifies that a value encrypted
// with the previous key (local-aes-v0) can still be decrypted by a provider
// that has validMasterKeyPrevious loaded as the "previous" key.
//
// Note: LocalAES.Rotate() is intentionally disabled (returns ErrNotImplemented).
// "Historical key" support is achieved by constructing a provider with both a
// current key and a previous key (GOCELL_MASTER_KEY + GOCELL_MASTER_KEY_PREVIOUS).
// The provider with both keys can decrypt values encrypted with either.
func TestValueTransformer_DecryptByHistoricalKeyID(t *testing.T) {
	ctx := context.Background()

	// Provider with both current and previous key loaded.
	// validMasterKey → "local-aes-v1" (current)
	// validMasterKeyPrevious → "local-aes-v0" (previous)
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, validMasterKeyPrevious)
	require.NoError(t, err)

	// Obtain the previous key handle directly to encrypt with the old key.
	previousHandle, err := p.ByID(ctx, crypto.LocalAESPreviousKeyID)
	require.NoError(t, err)

	plaintext := []byte("old-secret")
	aad := crypto.AADForConfig("config-core", "legacy_key")

	ct, nonce, edk, err := previousHandle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// The transformer should decrypt using the historical keyID.
	tr := crypto.NewValueTransformer(p)
	recovered, err := tr.Decrypt(ctx, ct, crypto.LocalAESPreviousKeyID, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestValueTransformer_DecryptWrongAAD_FailClosed
// ---------------------------------------------------------------------------

func TestValueTransformer_DecryptWrongAAD_FailClosed(t *testing.T) {
	ctx := context.Background()
	tr := newTestTransformer(t)

	plaintext := []byte("value")
	aad := crypto.AADForConfig("config-core", "my_key")
	wrongAAD := crypto.AADForConfig("config-core", "other_key")

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	_, err = tr.Decrypt(ctx, ct, keyID, nonce, edk, wrongAAD)
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// TestValueTransformer_UnknownKeyID_FailClosed
// ---------------------------------------------------------------------------

func TestValueTransformer_UnknownKeyID_FailClosed(t *testing.T) {
	ctx := context.Background()
	tr := newTestTransformer(t)

	plaintext := []byte("value")
	aad := crypto.AADForConfig("config-core", "my_key")
	ct, _, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	_, err = tr.Decrypt(ctx, ct, "nonexistent-key-id", nonce, edk, aad)
	require.Error(t, err, "unknown keyID must return an error (fail-closed)")
}

// ---------------------------------------------------------------------------
// TestNoopTransformer_Passthrough
// ---------------------------------------------------------------------------

func TestNoopTransformer_Passthrough(t *testing.T) {
	ctx := context.Background()
	tr := crypto.NoopTransformer{}

	plaintext := []byte("public-config-value")
	aad := crypto.AADForConfig("config-core", "public_key")

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, ct)
	assert.Empty(t, keyID)
	assert.Nil(t, nonce)
	assert.Nil(t, edk)

	recovered, err := tr.Decrypt(ctx, ct, keyID, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestAADForConfig_Format
// ---------------------------------------------------------------------------

func TestAADForConfig_Format(t *testing.T) {
	aad := crypto.AADForConfig("config-core", "api_key")
	assert.Equal(t, []byte("cell:config-core/key:api_key"), aad)
}

// ---------------------------------------------------------------------------
// ValueTransformer error paths (value_transformer.go uncovered branches)
// ---------------------------------------------------------------------------

// errorKeyProvider is a KeyProvider that always returns the configured error.
type errorKeyProvider struct {
	currentErr error
	byIDErr    error
}

func (p *errorKeyProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	return nil, p.currentErr
}

func (p *errorKeyProvider) ByID(_ context.Context, _ string) (crypto.KeyHandle, error) {
	return nil, p.byIDErr
}

func (p *errorKeyProvider) Rotate(_ context.Context) (string, error) {
	return "", nil
}

// errorKeyHandle is a KeyHandle whose Encrypt always returns the configured error.
type errorKeyHandle struct {
	encryptErr error
}

func (h *errorKeyHandle) ID() string { return "error-key" }

func (h *errorKeyHandle) Encrypt(_ context.Context, _, _ []byte) ([]byte, []byte, []byte, error) {
	return nil, nil, nil, h.encryptErr
}

func (h *errorKeyHandle) Decrypt(_ context.Context, _, _, _, _ []byte) ([]byte, error) {
	return nil, nil
}

// singleHandleProvider returns the given handle for Current().
type singleHandleProvider struct {
	handle crypto.KeyHandle
}

func (p *singleHandleProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	return p.handle, nil
}

func (p *singleHandleProvider) ByID(_ context.Context, _ string) (crypto.KeyHandle, error) {
	return nil, nil
}

func (p *singleHandleProvider) Rotate(_ context.Context) (string, error) {
	return "", nil
}

// TestValueTransformer_Encrypt_CurrentKeyError verifies that when
// provider.Current returns an error, Encrypt surfaces it.
func TestValueTransformer_Encrypt_CurrentKeyError(t *testing.T) {
	ctx := context.Background()
	p := &errorKeyProvider{currentErr: errors.New("current key unavailable")}
	tr := crypto.NewValueTransformer(p)

	_, _, _, _, err := tr.Encrypt(ctx, []byte("value"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get current key")
}

// TestValueTransformer_Encrypt_HandleEncryptError verifies that when
// handle.Encrypt returns an error, Encrypt surfaces it.
func TestValueTransformer_Encrypt_HandleEncryptError(t *testing.T) {
	ctx := context.Background()
	encErr := &errcode.Error{Code: errcode.ErrKeyProviderEncryptFailed, Message: "cipher error"}
	p := &singleHandleProvider{handle: &errorKeyHandle{encryptErr: encErr}}
	tr := crypto.NewValueTransformer(p)

	_, _, _, _, err := tr.Encrypt(ctx, []byte("value"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypt")
}

// TestValueTransformer_Decrypt_ByIDError verifies that when provider.ByID returns
// an error, Decrypt surfaces it.
func TestValueTransformer_Decrypt_ByIDError(t *testing.T) {
	ctx := context.Background()
	p := &errorKeyProvider{byIDErr: errors.New("key not found")}
	tr := crypto.NewValueTransformer(p)

	_, err := tr.Decrypt(ctx, []byte("ct"), "missing-key", nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve key")
}
