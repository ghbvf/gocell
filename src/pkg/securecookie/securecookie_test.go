package securecookie

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateKey(t *testing.T, n int) []byte {
	t.Helper()
	key := make([]byte, n)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func TestSecureCookie_SignOnly_RoundTrip(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	value := []byte("hello world")
	encoded, err := sc.Encode("test", value)
	require.NoError(t, err)

	decoded, err := sc.Decode("test", encoded)
	require.NoError(t, err)
	assert.Equal(t, value, decoded)
}

func TestSecureCookie_Encrypted_RoundTrip(t *testing.T) {
	hashKey := generateKey(t, 32)
	blockKey := generateKey(t, 32)
	sc, err := New(hashKey, blockKey)
	require.NoError(t, err)

	value := []byte("secret data")
	encoded, err := sc.Encode("sess", value)
	require.NoError(t, err)

	decoded, err := sc.Decode("sess", encoded)
	require.NoError(t, err)
	assert.Equal(t, value, decoded)
}

func TestSecureCookie_TamperedValue(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	encoded, err := sc.Encode("test", []byte("original"))
	require.NoError(t, err)

	mid := len(encoded) / 2
	tampered := encoded[:mid] + "X" + encoded[mid+1:]

	_, err = sc.Decode("test", tampered)
	assert.Error(t, err, "tampered value should fail decode")
}

func TestSecureCookie_Expired(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	sc = sc.WithMaxAge(1)

	encoded, err := sc.Encode("test", []byte("data"))
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond)

	_, err = sc.Decode("test", encoded)
	assert.ErrorIs(t, err, ErrExpired)
}

func TestSecureCookie_HashKeyTooShort(t *testing.T) {
	_, err := New([]byte("short"), nil)
	assert.ErrorIs(t, err, ErrHashKeyTooShort)
}

func TestSecureCookie_InvalidBlockKeyLength(t *testing.T) {
	hashKey := generateKey(t, 32)
	badBlockKey := generateKey(t, 15)
	_, err := New(hashKey, badBlockKey)
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "blockKey"), "error should mention blockKey")
}

func TestSecureCookie_EmptyValue_RoundTrip(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	encoded, err := sc.Encode("test", []byte{})
	require.NoError(t, err)

	decoded, err := sc.Decode("test", encoded)
	require.NoError(t, err)
	assert.Equal(t, []byte{}, decoded)
}

func TestSecureCookie_WrongName(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	encoded, err := sc.Encode("cookie-a", []byte("data"))
	require.NoError(t, err)

	_, err = sc.Decode("cookie-b", encoded)
	assert.ErrorIs(t, err, ErrHMACInvalid)
}

func TestSecureCookie_MaxAgeZero_NeverExpires(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	sc = sc.WithMaxAge(0)

	encoded, err := sc.Encode("test", []byte("data"))
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	decoded, err := sc.Decode("test", encoded)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), decoded)
}

func TestSecureCookie_AESKeySizes(t *testing.T) {
	hashKey := generateKey(t, 32)
	tests := []struct {
		name   string
		keyLen int
	}{
		{"AES-128", 16},
		{"AES-192", 24},
		{"AES-256", 32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blockKey := generateKey(t, tt.keyLen)
			sc, err := New(hashKey, blockKey)
			require.NoError(t, err)

			value := []byte("test-" + tt.name)
			encoded, err := sc.Encode("test", value)
			require.NoError(t, err)

			decoded, err := sc.Decode("test", encoded)
			require.NoError(t, err)
			assert.Equal(t, value, decoded)
		})
	}
}

func TestSecureCookie_Decode_MaliciousInput(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	tests := []struct {
		name    string
		encoded string
	}{
		{"empty string", ""},
		{"not base64", "!!!not-base64!!!"},
		{"too short (1 byte)", "AA"},
		{"exactly timestamp+mac minus 1", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"random garbage", "dGhpcyBpcyBub3QgYSB2YWxpZCBjb29raWU"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sc.Decode("test", tt.encoded)
			assert.Error(t, err, "malicious input should fail decode")
		})
	}
}

func TestSecureCookie_WithMaxAge_DeepCopyKeys(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	sc2 := sc.WithMaxAge(60)

	// Mutate original hashKey — should not affect sc2.
	hashKey[0] ^= 0xFF

	encoded, err := sc2.Encode("test", []byte("data"))
	require.NoError(t, err)

	decoded, err := sc2.Decode("test", encoded)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), decoded)
}
