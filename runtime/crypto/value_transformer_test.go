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

func TestValueTransformer_DecryptByHistoricalKeyID(t *testing.T) {
	ctx := context.Background()
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, "")
	require.NoError(t, err)
	tr := crypto.NewValueTransformer(p)

	plaintext := []byte("old-secret")
	aad := crypto.AADForConfig("config-core", "legacy_key")

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	oldKeyID := keyID

	// Rotate — current key advances.
	_, err = p.Rotate(ctx)
	require.NoError(t, err)

	// Decrypt with the historical keyID must still succeed.
	recovered, err := tr.Decrypt(ctx, ct, oldKeyID, nonce, edk, aad)
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
