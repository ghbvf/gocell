package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key, &key.PublicKey
}

func TestJWTVerifier_RS256_ValidToken(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	issuer := NewJWTIssuer(priv, "gocell", time.Hour)
	verifier := NewJWTVerifier(pub)

	tokenStr, err := issuer.Issue("user-1", []string{"admin", "user"}, []string{"api"})
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, "gocell", claims.Issuer)
	assert.Equal(t, []string{"admin", "user"}, claims.Roles)
	assert.Equal(t, []string{"api"}, claims.Audience)
	assert.False(t, claims.ExpiresAt.IsZero())
	assert.False(t, claims.IssuedAt.IsZero())
}

func TestJWTVerifier_RS256_ExpiredToken(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	issuer := NewJWTIssuer(priv, "gocell", -time.Hour) // already expired
	verifier := NewJWTVerifier(pub)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_RejectsHS256(t *testing.T) {
	// Create HS256 token and verify it is rejected by RS256 verifier.
	_, pub := generateTestKeyPair(t)
	verifier := NewJWTVerifier(pub)

	hmacToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := hmacToken.SignedString([]byte("some-secret"))
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_RejectsAlgNone(t *testing.T) {
	_, pub := generateTestKeyPair(t)
	verifier := NewJWTVerifier(pub)

	// Create an unsigned token with alg=none.
	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_WrongKey(t *testing.T) {
	priv1, _ := generateTestKeyPair(t)
	_, pub2 := generateTestKeyPair(t) // different key pair
	issuer := NewJWTIssuer(priv1, "gocell", time.Hour)
	verifier := NewJWTVerifier(pub2)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
}

func TestJWTVerifier_MalformedToken(t *testing.T) {
	_, pub := generateTestKeyPair(t)
	verifier := NewJWTVerifier(pub)

	_, err := verifier.Verify(context.Background(), "not.a.jwt")
	require.Error(t, err)
}

func TestJWTIssuer_RoundTrip(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	issuer := NewJWTIssuer(priv, "test-issuer", 30*time.Minute)
	verifier := NewJWTVerifier(pub)

	tokenStr, err := issuer.Issue("svc-audit", []string{"service"}, []string{"internal"})
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "svc-audit", claims.Subject)
	assert.Equal(t, "test-issuer", claims.Issuer)
	assert.Equal(t, []string{"service"}, claims.Roles)
	assert.Equal(t, []string{"internal"}, claims.Audience)
}

func TestJWTIssuer_NoRolesNoAudience(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	issuer := NewJWTIssuer(priv, "gocell", time.Hour)
	verifier := NewJWTVerifier(pub)

	tokenStr, err := issuer.Issue("user-2", nil, nil)
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-2", claims.Subject)
	assert.Empty(t, claims.Roles)
	assert.Empty(t, claims.Audience)
}

func TestLoadKeysFromEnv_PKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})

	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	t.Setenv(EnvJWTPrivateKey, string(privPEM))
	t.Setenv(EnvJWTPublicKey, string(pubPEM))

	priv, pub, err := LoadKeysFromEnv()
	require.NoError(t, err)
	assert.NotNil(t, priv)
	assert.NotNil(t, pub)
}
