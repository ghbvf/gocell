package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadKeySet_DevMode(t *testing.T) {
	ks, err := loadKeySet("")
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadKeySet_RealMode_MissingEnv(t *testing.T) {
	t.Setenv(auth.EnvJWTPrivateKey, "")
	t.Setenv(auth.EnvJWTPublicKey, "")

	_, err := loadKeySet("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), auth.EnvJWTPrivateKey)
}

func TestLoadKeySet_RealMode_Success(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "") // no previous key

	ks, err := loadKeySet("real")
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadKeySet_UnknownMode_FallsDev(t *testing.T) {
	// Typo or unknown value should still work (dev fallback) but logs a warning.
	ks, err := loadKeySet("reall") // deliberate typo
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestEnvOrDefault_WithEnv(t *testing.T) {
	t.Setenv("TEST_KEY_FOR_ENVDEFAULT", "actual-value")
	got := envOrDefault("TEST_KEY_FOR_ENVDEFAULT", "fallback")
	assert.Equal(t, []byte("actual-value"), got)
}

func TestEnvOrDefault_Fallback(t *testing.T) {
	t.Setenv("TEST_KEY_FOR_ENVDEFAULT_MISS", "")
	got := envOrDefault("TEST_KEY_FOR_ENVDEFAULT_MISS", "fallback")
	assert.Equal(t, []byte("fallback"), got)
}

// generateTestPEM creates a fresh 2048-bit RSA key pair as PEM bytes.
func generateTestPEM(t *testing.T) (privPEM, pubPEM []byte) {
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
