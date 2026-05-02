package securecookie

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// fixedClock is a test-local Clock implementation that returns a fixed time.
// pkg/ cannot import kernel/clockmock (LAYER-01), so tests define their own
// minimal stub.
type fixedClock struct {
	t time.Time
}

func (c *fixedClock) Now() time.Time { return c.t }

// newFixedClock returns a fixedClock set to a stable test epoch.
func newFixedClock() *fixedClock {
	return &fixedClock{t: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
}

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
	sc = sc.WithClock(newFixedClock())

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
	sc = sc.WithClock(newFixedClock())

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
	sc = sc.WithClock(newFixedClock())

	encoded, err := sc.Encode("test", []byte("original"))
	require.NoError(t, err)

	mid := len(encoded) / 2
	// Bit-flip guarantees the byte always changes (fixes 1/64 flaky when encoded[mid] was already 'X').
	tampered := encoded[:mid] + string(encoded[mid]^1) + encoded[mid+1:]

	_, err = sc.Decode("test", tampered)
	assert.Error(t, err, "tampered value should fail decode")
}

func TestSecureCookie_Expired(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	clk := newFixedClock()
	sc = sc.WithMaxAge(1).WithClock(clk)

	encoded, err := sc.Encode("test", []byte("data"))
	require.NoError(t, err)

	// Advance clock by 2 seconds to exceed max-age=1.
	clk.t = clk.t.Add(testtime.D2s)

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
	sc = sc.WithClock(newFixedClock())

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
	sc = sc.WithClock(newFixedClock())

	encoded, err := sc.Encode("cookie-a", []byte("data"))
	require.NoError(t, err)

	_, err = sc.Decode("cookie-b", encoded)
	assert.ErrorIs(t, err, ErrHMACInvalid)
}

func TestSecureCookie_MaxAgeZero_NeverExpires(t *testing.T) {
	hashKey := generateKey(t, 32)
	sc, err := New(hashKey, nil)
	require.NoError(t, err)

	clk := newFixedClock()
	sc = sc.WithMaxAge(0).WithClock(clk)

	encoded, err := sc.Encode("test", []byte("data"))
	require.NoError(t, err)

	// Advance clock by a large amount — maxAge=0 means no expiry check.
	clk.t = clk.t.Add(testtime.MediumPoll)
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
			sc = sc.WithClock(newFixedClock())

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
	sc = sc.WithClock(newFixedClock())

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

	sc2 := sc.WithMaxAge(60).WithClock(newFixedClock())

	// Mutate original hashKey — should not affect sc2.
	hashKey[0] ^= 0xFF

	encoded, err := sc2.Encode("test", []byte("data"))
	require.NoError(t, err)

	decoded, err := sc2.Decode("test", encoded)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), decoded)
}
