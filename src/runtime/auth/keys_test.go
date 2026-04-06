package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	// Generate a 1024-bit RSA key (below MinRSAKeyBits).
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)

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
