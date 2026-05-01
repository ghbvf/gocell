package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// weak1024KeyPEM is a pre-generated 1024-bit RSA private key (PKCS#8 DER,
// PEM-encoded). Used only in tests that verify weak-key rejection; avoids
// calling rsa.GenerateKey with a substandard bit size at test runtime.
const weak1024KeyPEM = `-----BEGIN PRIVATE KEY-----
MIICdwIBADANBgkqhkiG9w0BAQEFAASCAmEwggJdAgEAAoGBAN3/s6cdoda0Yn9a
wCHSWfWu9L75nMJvJsLQ8gI+vuPWdYoOF3VSp+HV+ojNBO5YTstQJVnSJc6Jjn8K
mufo4l2ar1mEBZkUFFMnC20wOlmX9zhQc34XWF9Kow5naZ8rB3OeJQn2z211sFmw
BK6DqaNWS54sEK8fA9bRp4i0eFLvAgMBAAECgYEA1gnaWc7dIdgre2SxCCr6p0D3
IkYiGOj38y9nljiO7bbw/plVjr2RtdEMS+d30KF93tK4IGDYKMlBhUVhUyWbUR6H
Bcret/WG8ElINe/k3CpjbM/hji5lZPCQ1CxC8qNP4MnR7n5jh7nSYb1gCNHZItdO
yvfSuKjGEj3fntBbfEECQQD2Y3GW7zWJ7MlNtUFuqXSLqzTg1KO7261WI+n4E4+4
yzHzrZKOg7UWiW3zpxMm8tEmAVrx8RQhGtRptLmhXq+hAkEA5qiw/BBLziBV8dqk
DUS6Ft5ttXM5/QZawGAfDd+k/SVaaM9K+DlZsxwxwAuYqACyXdq3bb+du5CBS00V
vYk4jwJBAL+U/Yr+P6QagUCyMsmoa936Zyh3T0VQgEydqlziYPuwzAuNKIs2MEXw
4JT3kbXUUvp5TU0ZRqyjHw1+oGSwqmECQDV6qUZYFOteze58bgrxg1/oBHHMnIZQ
4du2rZyO3PcgoPyqC0zQJz8C63oGdkeFmdVu75aPlee2EnQ+FCtU1HsCQFR7HUue
Ki25u0uMIoworRTfNUh1kyDKnI8CYIQNcCLDldTf8cZexPhMLb43qCM3lz3L+OMB
qZvnvePyPUu8ytI=
-----END PRIVATE KEY-----`

// loadWeak1024Key decodes weak1024KeyPEM into an *rsa.PrivateKey.
// It must only be used in tests that verify weak-key rejection logic.
func loadWeak1024Key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	block, _ := pem.Decode([]byte(weak1024KeyPEM))
	if block == nil {
		t.Fatal("loadWeak1024Key: pem.Decode returned nil")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("loadWeak1024Key: ParsePKCS8PrivateKey: %v", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("loadWeak1024Key: expected *rsa.PrivateKey, got %T", key)
	}
	return rsaKey
}

// --- Phase 1: Foundational (T001-T004) ---

func TestThumbprint_Deterministic(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	_ = priv

	kid1 := Thumbprint(pub)
	kid2 := Thumbprint(pub)
	assert.Equal(t, kid1, kid2, "same key must produce same thumbprint")
	assert.NotEmpty(t, kid1)
}

func TestThumbprint_DifferentKeys(t *testing.T) {
	_, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	kid1 := Thumbprint(pub1)
	kid2 := Thumbprint(pub2)
	assert.NotEqual(t, kid1, kid2, "different keys must produce different thumbprints")
}

func TestThumbprint_OutputLength(t *testing.T) {
	// RFC 7638 SHA-256 thumbprint: 32 bytes → 43 chars base64url (no padding).
	_, pub := generateTestKeyPair(t)
	kid := Thumbprint(pub)
	assert.Len(t, kid, 43, "SHA-256 base64url-encoded without padding is always 43 chars")
}

func TestThumbprint_Base64URLEncoded(t *testing.T) {
	_, pub := generateTestKeyPair(t)
	kid := Thumbprint(pub)

	// Base64url uses no padding and only URL-safe characters.
	assert.NotContains(t, kid, "+")
	assert.NotContains(t, kid, "/")
	assert.NotContains(t, kid, "=")
}

func TestNewKeySet_SingleKey(t *testing.T) {
	priv, pub := generateTestKeyPair(t)

	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	assert.NotNil(t, ks)
	assert.Equal(t, Thumbprint(pub), ks.SigningKeyID())
	assert.Equal(t, priv, ks.SigningKey())
}

func TestNewKeySet_PublicKeyByKID(t *testing.T) {
	priv, pub := generateTestKeyPair(t)

	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)

	got, err := ks.PublicKeyByKID(ks.SigningKeyID())
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

func TestNewKeySet_UnknownKID(t *testing.T) {
	priv, pub := generateTestKeyPair(t)

	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)

	_, err = ks.PublicKeyByKID("nonexistent-kid")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthKeyInvalid, ecErr.Code)
}

func TestNewKeySet_NilKeyReturnsError(t *testing.T) {
	_, err := NewKeySet(nil, nil)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthKeyInvalid, ecErr.Code)
}

func TestNewKeySet_MismatchedKeyPairReturnsError(t *testing.T) {
	priv1, _ := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	_, err := NewKeySet(priv1, pub2) // private from pair 1, public from pair 2
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not form a valid pair")
}

func TestNewKeySet_WeakKeyReturnsError(t *testing.T) {
	weakKey := loadWeak1024Key(t)

	_, err := NewKeySet(weakKey, &weakKey.PublicKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1024")
}

func TestNewKeySetWithVerificationKeys_RejectsWeakKey(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	weakKey := loadWeak1024Key(t)

	vk := VerificationKey{
		PublicKey: &weakKey.PublicKey,
		KeyID:     Thumbprint(&weakKey.PublicKey),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	_, err := NewKeySetWithVerificationKeys(priv, pub, []VerificationKey{vk})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1024")
	assert.Contains(t, err.Error(), "verification")
}

func TestNewKeySetWithVerificationKeys_RejectsEmptyKeyID(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     "",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	_, err := NewKeySetWithVerificationKeys(priv, pub, []VerificationKey{vk})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestNewKeySetWithVerificationKeys_RejectsNilPublicKey(t *testing.T) {
	priv, pub := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: nil,
		KeyID:     "nil-key",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	_, err := NewKeySetWithVerificationKeys(priv, pub, []VerificationKey{vk})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

// --- Phase 3: User Story 2 (T011-T016) ---

func TestKeySet_VerificationKeyLookup(t *testing.T) {
	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk})
	require.NoError(t, err)

	// Can look up verification key by kid.
	got, err := ks.PublicKeyByKID(vk.KeyID)
	require.NoError(t, err)
	assert.Equal(t, pub2, got)

	// Can still look up signing key.
	got, err = ks.PublicKeyByKID(ks.SigningKeyID())
	require.NoError(t, err)
	assert.Equal(t, pub1, got)
}

func TestKeySet_OnlySignsWithActiveKey(t *testing.T) {
	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk})
	require.NoError(t, err)

	// SigningKeyID must be the active key, not the verification key.
	assert.Equal(t, Thumbprint(pub1), ks.SigningKeyID())
	assert.NotEqual(t, vk.KeyID, ks.SigningKeyID())
}

func TestKeySet_PruneExpiredKeys(t *testing.T) {
	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	// Create verification key that is already expired.
	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(-time.Second),
	}

	// Already-expired keys are pruned at construction time.
	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk})
	require.NoError(t, err)

	_, err = ks.PublicKeyByKID(vk.KeyID)
	require.Error(t, err, "expired key should have been pruned")
}

func TestKeySet_PruneExpired_AfterTimeAdvance(t *testing.T) {
	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	baseTime := time.Now()
	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: baseTime.Add(time.Hour),
	}

	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk})
	require.NoError(t, err)

	// Key should be accessible before expiry.
	got, err := ks.PublicKeyByKID(vk.KeyID)
	require.NoError(t, err)
	assert.Equal(t, pub2, got)

	// Advance clock past expiry using injectable now func.
	WithKeySetClock(func() time.Time { return baseTime.Add(2 * time.Hour) })(ks)

	// Key should be pruned now.
	_, err = ks.PublicKeyByKID(vk.KeyID)
	require.Error(t, err, "key should be pruned after expiry")
}

func TestKeySet_ZeroExpiryPrunesImmediately(t *testing.T) {
	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Time{}, // zero value
	}

	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk})
	require.NoError(t, err)

	_, err = ks.PublicKeyByKID(vk.KeyID)
	require.Error(t, err, "zero-expiry key should be pruned immediately")
}

func TestKeySet_RapidRotationReplacesOldest(t *testing.T) {
	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)
	_, pub3 := generateTestKeyPair(t)

	vk1 := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	vk2 := VerificationKey{
		PublicKey: pub3,
		KeyID:     Thumbprint(pub3),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	// Both verification keys should be present.
	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk1, vk2})
	require.NoError(t, err)

	got1, err := ks.PublicKeyByKID(vk1.KeyID)
	require.NoError(t, err)
	assert.Equal(t, pub2, got1)

	got2, err := ks.PublicKeyByKID(vk2.KeyID)
	require.NoError(t, err)
	assert.Equal(t, pub3, got2)
}

func TestLoadKeySetFromEnv_ActiveOnly(t *testing.T) {
	privPEM, pubPEM := generateTestKeyPairPEM(t)

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))
	t.Setenv(EnvJWTPrevPublicKey, "")
	t.Setenv(EnvJWTPrevKeyExpires, "")

	ks, err := LoadKeySetFromEnv()
	require.NoError(t, err)
	assert.NotEmpty(t, ks.SigningKeyID())
}

func TestLoadKeySetFromEnv_WithVerificationKey(t *testing.T) {
	privPEM, pubPEM := generateTestKeyPairPEM(t)
	_, prevPubPEM := generateTestKeyPairPEM(t)

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))
	t.Setenv(EnvJWTPrevPublicKey, string(prevPubPEM))
	t.Setenv(EnvJWTPrevKeyExpires, time.Now().Add(time.Hour).Format(time.RFC3339))

	ks, err := LoadKeySetFromEnv()
	require.NoError(t, err)
	assert.NotEmpty(t, ks.SigningKeyID())

	// Verification key should be accessible.
	prevPub, err := parseRSAPublicKey(prevPubPEM)
	require.NoError(t, err)
	prevKID := Thumbprint(prevPub)

	got, err := ks.PublicKeyByKID(prevKID)
	require.NoError(t, err)
	assert.Equal(t, prevPub, got)
}

func TestLoadKeySetFromEnv_MissingActiveFails(t *testing.T) {
	t.Setenv(EnvJWTPrivateKey, "")
	t.Setenv(EnvJWTPublicKey, "")

	_, err := LoadKeySetFromEnv()
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrKeyMissing, ecErr.Code)
}

func TestLoadKeySetFromEnv_PrevKeyMissingExpiryFails(t *testing.T) {
	privPEM, pubPEM := generateTestKeyPairPEM(t)
	_, prevPubPEM := generateTestKeyPairPEM(t)

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))
	t.Setenv(EnvJWTPrevPublicKey, string(prevPubPEM))
	t.Setenv(EnvJWTPrevKeyExpires, "") // missing

	_, err := LoadKeySetFromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), EnvJWTPrevKeyExpires)
}

func TestLoadKeySetFromEnv_InvalidPrevPEMFails(t *testing.T) {
	privPEM, pubPEM := generateTestKeyPairPEM(t)

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))
	t.Setenv(EnvJWTPrevPublicKey, "not-valid-pem")
	t.Setenv(EnvJWTPrevKeyExpires, time.Now().Add(time.Hour).Format(time.RFC3339))

	_, err := LoadKeySetFromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PEM block")
}

func TestLoadKeySetFromEnv_InvalidExpiryFails(t *testing.T) {
	privPEM, pubPEM := generateTestKeyPairPEM(t)
	_, prevPubPEM := generateTestKeyPairPEM(t)

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))
	t.Setenv(EnvJWTPrevPublicKey, string(prevPubPEM))
	t.Setenv(EnvJWTPrevKeyExpires, "not-a-date")

	_, err := LoadKeySetFromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RFC 3339")
}

// --- Phase 5: User Story 4 (T026-T029) ---

func TestKeySet_LifecycleLog_Activation(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	priv, pub := generateTestKeyPair(t)
	_, err := NewKeySet(priv, pub, WithKeySetLogger(logger))
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "key activated")
	assert.Contains(t, output, Thumbprint(pub))
}

func TestKeySet_LifecycleLog_VerificationOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	_, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk}, WithKeySetLogger(logger))
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "verification-only")
	assert.Contains(t, output, Thumbprint(pub2))
}

func TestKeySet_LifecycleLog_Pruning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	priv1, pub1 := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	baseTime := time.Now()
	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: baseTime.Add(time.Hour),
	}

	ks, err := NewKeySetWithVerificationKeys(priv1, pub1, []VerificationKey{vk}, WithKeySetLogger(logger))
	require.NoError(t, err)

	// Advance clock past expiry.
	WithKeySetClock(func() time.Time { return baseTime.Add(2 * time.Hour) })(ks)

	// Reset buffer so only PruneExpired log is captured.
	buf.Reset()

	ks.PruneExpired()

	output := buf.String()
	assert.Contains(t, output, "key pruned")
	assert.Contains(t, output, Thumbprint(pub2))
}

// --- Concurrency (F1.4 + F3.1) ---

func TestKeySet_ConcurrentPublicKeyByKID(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	ks, err := NewKeySetWithVerificationKeys(priv, pub, []VerificationKey{vk})
	require.NoError(t, err)

	// Run concurrent lookups — go test -race will detect data races.
	const goroutines = 50
	done := make(chan struct{})
	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 100 {
				_, _ = ks.PublicKeyByKID(ks.SigningKeyID())
				_, _ = ks.PublicKeyByKID(vk.KeyID)
			}
		}()
	}
	for range goroutines {
		<-done
	}
}

func TestKeySet_ConcurrentPruneExpiredAndRead(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t)

	vk := VerificationKey{
		PublicKey: pub2,
		KeyID:     Thumbprint(pub2),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	ks, err := NewKeySetWithVerificationKeys(priv, pub, []VerificationKey{vk})
	require.NoError(t, err)

	// Run PruneExpired (write lock) concurrently with PublicKeyByKID (read lock).
	// go test -race will detect data races if locking is broken.
	const goroutines = 20
	done := make(chan struct{})
	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 100 {
				_, _ = ks.PublicKeyByKID(ks.SigningKeyID())
				_, _ = ks.PublicKeyByKID(vk.KeyID)
			}
		}()
	}
	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 50 {
				ks.PruneExpired()
			}
		}()
	}
	for range goroutines * 2 {
		<-done
	}
}

// --- Existing tests (preserved) ---

func TestLoadKeysFromEnv_BothMissing(t *testing.T) {
	t.Setenv(EnvJWTPrivateKey, "")
	t.Setenv(EnvJWTPublicKey, "")

	_, _, err := LoadKeysFromEnv()
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrKeyMissing, ecErr.Code)
	assert.Contains(t, ecErr.Message, EnvJWTPrivateKey)
}

func TestLoadKeysFromEnv_PublicMissing(t *testing.T) {
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, "")

	_, _, err := LoadKeysFromEnv()
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrKeyMissing, ecErr.Code)
	assert.Contains(t, ecErr.Message, EnvJWTPublicKey)
}

func TestLoadKeysFromEnv_InvalidPEM(t *testing.T) {
	t.Setenv(EnvJWTPrivateKey, "not-valid-pem")
	t.Setenv(EnvJWTPublicKey, "also-not-valid")

	_, _, err := LoadKeysFromEnv()
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrKeyMissing, ecErr.Code)
}

func TestLoadKeysFromEnv_ValidKeys(t *testing.T) {
	privKey, pubKey := generateTestKeyPairPEM(t)

	t.Setenv(EnvJWTPrivateKey, string(privKey))
	t.Setenv(EnvJWTPublicKey, string(pubKey))

	priv, pub, err := LoadKeysFromEnv()
	require.NoError(t, err)
	assert.NotNil(t, priv)
	assert.NotNil(t, pub)
}

func TestLoadRSAKeyPairFromPEM_RejectsWeakKey(t *testing.T) {
	// Use a pre-generated 1024-bit RSA key (below MinRSAKeyBits).
	weakKey := loadWeak1024Key(t)

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(weakKey),
	})
	pubBytes, err := x509.MarshalPKIXPublicKey(&weakKey.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})

	// Private key parse should fail due to weak key.
	_, _, err = LoadRSAKeyPairFromPEM(privPEM, pubPEM)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthKeyInvalid, ecErr.Code)
	assert.Contains(t, ecErr.Message, "1024")
}

func generateTestKeyPairPEM(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	return privPEM, pubPEM
}
