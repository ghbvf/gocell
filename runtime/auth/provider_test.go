package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface checks.
var _ KeyProvider = (*EnvKeyProvider)(nil)
var _ KeyProvider = (*StaticKeyProvider)(nil)

// --- EnvKeyProvider tests ---

func TestEnvKeyProvider_RSAKeySet_ReturnsLoadedKeySet(t *testing.T) {
	setJWTEnvVars(t)

	p := NewEnvKeyProvider()
	ks, err := p.RSAKeySet()
	require.NoError(t, err)
	require.NotNil(t, ks)
	assert.NotEmpty(t, ks.SigningKeyID())
}

func TestEnvKeyProvider_RSAKeySet_WithVerificationKey(t *testing.T) {
	priv, pub := setJWTEnvVars(t)
	_ = priv

	// Add a previous verification key.
	prevPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	prevPubBytes, err := x509.MarshalPKIXPublicKey(&prevPriv.PublicKey)
	require.NoError(t, err)
	prevPubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: prevPubBytes})
	t.Setenv(EnvJWTPrevPublicKey, string(prevPubPEM))
	t.Setenv(EnvJWTPrevKeyExpires, time.Now().Add(time.Hour).Format(time.RFC3339))

	p := NewEnvKeyProvider()
	ks, err := p.RSAKeySet()
	require.NoError(t, err)

	// Active key accessible.
	kid := Thumbprint(pub)
	gotPub, err := ks.PublicKeyByKID(kid)
	require.NoError(t, err)
	assert.Equal(t, pub, gotPub)

	// Previous key accessible.
	prevKID := Thumbprint(&prevPriv.PublicKey)
	gotPrev, err := ks.PublicKeyByKID(prevKID)
	require.NoError(t, err)
	assert.Equal(t, &prevPriv.PublicKey, gotPrev)
}

func TestEnvKeyProvider_RSAKeySet_MissingKeysFails(t *testing.T) {
	// Set JWT env vars to empty to simulate missing configuration.
	t.Setenv(EnvJWTPrivateKey, "")
	t.Setenv(EnvJWTPublicKey, "")

	p := NewEnvKeyProvider()
	ks, err := p.RSAKeySet()
	assert.Nil(t, ks)
	assert.Error(t, err)
}

func TestEnvKeyProvider_HMACKeyRing_ReturnsLoadedRing(t *testing.T) {
	secret := "this-is-a-32-byte-secret-for-hmac!"
	t.Setenv(EnvServiceSecret, secret)

	p := NewEnvKeyProvider()
	ring, err := p.HMACKeyRing()
	require.NoError(t, err)
	require.NotNil(t, ring)
	assert.Equal(t, []byte(secret), ring.Current())
}

func TestEnvKeyProvider_HMACKeyRing_WithPrevious(t *testing.T) {
	current := "current-secret-at-least-32-bytes!"
	previous := "previous-secret-at-least-32-byte!"
	t.Setenv(EnvServiceSecret, current)
	t.Setenv(EnvServiceSecretPrevious, previous)

	p := NewEnvKeyProvider()
	ring, err := p.HMACKeyRing()
	require.NoError(t, err)
	secrets := ring.Secrets()
	assert.Len(t, secrets, 2)
}

func TestEnvKeyProvider_HMACKeyRing_NotConfiguredReturnsError(t *testing.T) {
	t.Setenv(EnvServiceSecret, "")

	p := NewEnvKeyProvider()
	ring, err := p.HMACKeyRing()
	assert.Nil(t, ring)
	assert.Error(t, err)
}

func TestEnvKeyProvider_OnlyRSA_HMACReturnsError(t *testing.T) {
	setJWTEnvVars(t)
	t.Setenv(EnvServiceSecret, "")

	p := NewEnvKeyProvider()

	ks, err := p.RSAKeySet()
	require.NoError(t, err)
	assert.NotNil(t, ks)

	ring, err := p.HMACKeyRing()
	assert.Nil(t, ring)
	assert.Error(t, err)
}

func TestEnvKeyProvider_OnlyHMAC_RSAReturnsError(t *testing.T) {
	t.Setenv(EnvJWTPrivateKey, "")
	t.Setenv(EnvJWTPublicKey, "")
	t.Setenv(EnvServiceSecret, "this-is-a-32-byte-secret-for-hmac!")

	p := NewEnvKeyProvider()

	ks, err := p.RSAKeySet()
	assert.Nil(t, ks)
	assert.Error(t, err)

	ring, err := p.HMACKeyRing()
	require.NoError(t, err)
	assert.NotNil(t, ring)
}

func TestEnvKeyProvider_RSAKeySet_ReturnsSameInstance(t *testing.T) {
	setJWTEnvVars(t)

	p := NewEnvKeyProvider()
	ks1, err := p.RSAKeySet()
	require.NoError(t, err)
	require.NotNil(t, ks1)
	ks2, err := p.RSAKeySet()
	require.NoError(t, err)
	assert.Same(t, ks1, ks2, "RSAKeySet must return the same cached instance")
}

func TestEnvKeyProvider_HMACKeyRing_ReturnsSameInstance(t *testing.T) {
	t.Setenv(EnvServiceSecret, "this-is-a-32-byte-secret-for-hmac!")

	p := NewEnvKeyProvider()
	r1, err := p.HMACKeyRing()
	require.NoError(t, err)
	require.NotNil(t, r1)
	r2, err := p.HMACKeyRing()
	require.NoError(t, err)
	assert.Same(t, r1, r2, "HMACKeyRing must return the same cached instance")
}

// --- StaticKeyProvider tests ---

func TestStaticKeyProvider_ReturnsProvidedKeySet(t *testing.T) {
	ks, _, _ := MustNewTestKeySet()
	p := NewStaticKeyProvider(ks, nil)

	got, err := p.RSAKeySet()
	require.NoError(t, err)
	assert.Same(t, ks, got)
}

func TestStaticKeyProvider_ReturnsProvidedHMACKeyRing(t *testing.T) {
	ring, err := NewHMACKeyRing([]byte("a-32-byte-secret-for-test-hmac!!"), nil)
	require.NoError(t, err)
	p := NewStaticKeyProvider(nil, ring)

	got, err := p.HMACKeyRing()
	require.NoError(t, err)
	assert.Same(t, ring, got)
}

func TestStaticKeyProvider_NilKeySetReturnsError(t *testing.T) {
	p := NewStaticKeyProvider(nil, nil)
	ks, err := p.RSAKeySet()
	assert.Nil(t, ks)
	assert.Error(t, err)
}

func TestStaticKeyProvider_NilHMACKeyRingReturnsError(t *testing.T) {
	p := NewStaticKeyProvider(nil, nil)
	ring, err := p.HMACKeyRing()
	assert.Nil(t, ring)
	assert.Error(t, err)
}

// --- MustNewTestKeyProvider tests ---

func TestMustNewTestKeyProvider_ReturnsValidProvider(t *testing.T) {
	p := MustNewTestKeyProvider()
	require.NotNil(t, p)

	ks, err := p.RSAKeySet()
	require.NoError(t, err)
	assert.NotNil(t, ks)
	assert.NotEmpty(t, ks.SigningKeyID())

	ring, err := p.HMACKeyRing()
	require.NoError(t, err)
	assert.NotNil(t, ring)
	assert.True(t, len(ring.Current()) >= MinHMACKeyBytes)
}

// --- helpers ---

// setJWTEnvVars generates a key pair and sets JWT env vars for testing.
// Returns the generated private/public key pair.
func setJWTEnvVars(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pub := &priv.PublicKey

	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})

	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))

	return priv, pub
}
