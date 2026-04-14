package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
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

func mustTestKeySet(t *testing.T) *KeySet {
	t.Helper()
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	return ks
}

// --- Phase 2: User Story 1 (T005-T010) ---

func TestJWTIssuer_TokenHasKID(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	// Decode the token header to check kid.
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3)

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	assert.Contains(t, string(headerJSON), `"kid"`)
	assert.Contains(t, string(headerJSON), ks.SigningKeyID())
}

func TestJWTIssuer_KIDMatchesThumbprint(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)

	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	// Parse without verification to inspect header.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	require.NoError(t, err)

	kid, ok := token.Header["kid"].(string)
	require.True(t, ok)
	assert.Equal(t, Thumbprint(pub), kid)
}

func TestJWTVerifier_VerifiesByKID(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", []string{"admin"}, []string{"api"})
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, "gocell", claims.Issuer)
	assert.Equal(t, []string{"admin"}, claims.Roles)
	assert.Equal(t, []string{"api"}, claims.Audience)
}

func TestJWTVerifier_RejectsUnknownKID(t *testing.T) {
	ks1 := mustTestKeySet(t)
	ks2 := mustTestKeySet(t)

	issuer, err := NewJWTIssuer(ks1, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks2) // different key set
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_RejectsMissingKID(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	// Create a token WITHOUT kid header (legacy-style).
	claims := jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	// Deliberately do NOT set token.Header["kid"]
	tokenStr, err := token.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

// --- Updated existing tests ---

func TestJWTVerifier_RS256_ValidToken(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

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
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", -time.Hour) // already expired
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_RejectsHS256(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

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
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

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

func TestJWTVerifier_RejectsRS384(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	// Sign a valid token with RS384 instead of RS256.
	token := jwt.NewWithClaims(jwt.SigningMethodRS384, jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = ks.SigningKeyID()
	tokenStr, err := token.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_RejectsRS512(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	token := jwt.NewWithClaims(jwt.SigningMethodRS512, jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = ks.SigningKeyID()
	tokenStr, err := token.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestJWTVerifier_WrongKey(t *testing.T) {
	ks1 := mustTestKeySet(t)
	ks2 := mustTestKeySet(t)

	issuer, err := NewJWTIssuer(ks1, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks2)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), tokenStr)
	require.Error(t, err)
}

func TestJWTVerifier_MalformedToken(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), "not.a.jwt")
	require.Error(t, err)
}

func TestJWTIssuer_RoundTrip(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "test-issuer", 30*time.Minute)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

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
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-2", nil, nil)
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-2", claims.Subject)
	assert.Empty(t, claims.Roles)
	assert.Empty(t, claims.Audience)
}

func TestNewJWTVerifier_NilKeySetReturnsError(t *testing.T) {
	_, err := NewJWTVerifier(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_KEY_INVALID")
}

func TestNewJWTIssuer_NilKeySetReturnsError(t *testing.T) {
	_, err := NewJWTIssuer(nil, "gocell", time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_KEY_INVALID")
}

// --- Multi-key verification (US2 via JWT) ---

func TestJWTVerifier_AcceptsVerificationOnlyKey(t *testing.T) {
	// Key pair 1: the OLD key (will become verification-only).
	priv1, pub1 := generateTestKeyPair(t)
	// Key pair 2: the NEW active key.
	priv2, pub2 := generateTestKeyPair(t)

	// Build a KeySet with key2 as active, key1 as verification-only.
	vk := VerificationKey{
		PublicKey: pub1,
		KeyID:     Thumbprint(pub1),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	ks, err := NewKeySetWithVerificationKeys(priv2, pub2, []VerificationKey{vk})
	require.NoError(t, err)

	// Issue a token signed with the OLD key (key1), adding kid manually.
	oldClaims := jwt.MapClaims{
		"sub": "user-old",
		"iss": "gocell",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	oldToken := jwt.NewWithClaims(jwt.SigningMethodRS256, oldClaims)
	oldToken.Header["kid"] = Thumbprint(pub1)
	oldTokenStr, err := oldToken.SignedString(priv1)
	require.NoError(t, err)

	// Verifier using the new KeySet should still accept the old token.
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), oldTokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-old", claims.Subject)

	// New tokens use the new key.
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)

	newTokenStr, err := issuer.Issue("user-new", nil, nil)
	require.NoError(t, err)

	claims, err = verifier.Verify(context.Background(), newTokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-new", claims.Subject)
}

// --- Interface abstraction tests (WM-2-F1) ---

// Compile-time checks: *KeySet satisfies both interfaces.
var _ SigningKeyProvider = (*KeySet)(nil)
var _ VerificationKeyStore = (*KeySet)(nil)

// stubSigningKeyProvider is a minimal test double for SigningKeyProvider.
type stubSigningKeyProvider struct {
	key *rsa.PrivateKey
	kid string
}

func (s *stubSigningKeyProvider) SigningKey() *rsa.PrivateKey { return s.key }
func (s *stubSigningKeyProvider) SigningKeyID() string        { return s.kid }

// stubVerificationKeyStore is a minimal test double for VerificationKeyStore.
type stubVerificationKeyStore struct {
	keys map[string]*rsa.PublicKey
}

func (s *stubVerificationKeyStore) PublicKeyByKID(kid string) (*rsa.PublicKey, error) {
	pub, ok := s.keys[kid]
	if !ok {
		return nil, fmt.Errorf("unknown kid: %s", kid)
	}
	return pub, nil
}

func TestJWTIssuer_AcceptsSigningKeyProvider(t *testing.T) {
	priv, _ := generateTestKeyPair(t)
	stub := &stubSigningKeyProvider{key: priv, kid: "test-kid-001"}

	issuer, err := NewJWTIssuer(stub, "gocell-test", time.Hour)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue("user-1", []string{"admin"}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, tokenStr)

	// Verify the kid in token header matches stub's kid.
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3)
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	assert.Contains(t, string(headerJSON), "test-kid-001")
}

func TestJWTIssuer_EmptyKID_ProducesTokenWithEmptyKID(t *testing.T) {
	priv, _ := generateTestKeyPair(t)
	stub := &stubSigningKeyProvider{key: priv, kid: ""}

	issuer, err := NewJWTIssuer(stub, "gocell-test", time.Hour)
	require.NoError(t, err)

	// Issue succeeds but produces a token with empty kid — verifier would reject it.
	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, tokenStr)
}

func TestJWTIssuer_NilKey_FailsToSign(t *testing.T) {
	stub := &stubSigningKeyProvider{key: nil, kid: "some-kid"}

	issuer, err := NewJWTIssuer(stub, "gocell-test", time.Hour)
	require.NoError(t, err)

	// Sign should fail because the key is nil.
	_, err = issuer.Issue("user-1", nil, nil)
	require.Error(t, err)
}

func TestJWTVerifier_AcceptsVerificationKeyStore(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	kid := Thumbprint(pub)

	// Issue a token with the real key.
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	issuer, err := NewJWTIssuer(ks, "gocell-test", time.Hour)
	require.NoError(t, err)
	tokenStr, err := issuer.Issue("user-1", nil, nil)
	require.NoError(t, err)

	// Verify using a stub store with only the public key.
	stub := &stubVerificationKeyStore{keys: map[string]*rsa.PublicKey{kid: pub}}
	verifier, err := NewJWTVerifier(stub)
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
}

func TestNewJWTIssuer_NilSigningKeyProvider(t *testing.T) {
	_, err := NewJWTIssuer(nil, "gocell", time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing key provider")
}

func TestNewJWTVerifier_NilVerificationKeyStore(t *testing.T) {
	_, err := NewJWTVerifier(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verification key store")
}

func TestMapClaimsToClaims_EdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		claims jwt.MapClaims
		check  func(t *testing.T, c Claims)
	}{
		{
			name:   "empty claims",
			claims: jwt.MapClaims{},
			check: func(t *testing.T, c Claims) {
				assert.Empty(t, c.Subject)
				assert.Empty(t, c.Issuer)
				assert.Nil(t, c.Audience)
				assert.Nil(t, c.Roles)
				assert.True(t, c.ExpiresAt.IsZero())
			},
		},
		{
			name:   "string audience",
			claims: jwt.MapClaims{"aud": "single-aud"},
			check: func(t *testing.T, c Claims) {
				assert.Equal(t, []string{"single-aud"}, c.Audience)
			},
		},
		{
			name:   "array audience with non-string elements",
			claims: jwt.MapClaims{"aud": []any{"valid", 42, "also-valid"}},
			check: func(t *testing.T, c Claims) {
				assert.Equal(t, []string{"valid", "also-valid"}, c.Audience,
					"non-string audience elements should be silently skipped")
			},
		},
		{
			name:   "roles with non-string elements",
			claims: jwt.MapClaims{"roles": []any{"admin", 123, "user"}},
			check: func(t *testing.T, c Claims) {
				assert.Equal(t, []string{"admin", "user"}, c.Roles,
					"non-string role elements should be silently skipped")
			},
		},
		{
			name:   "numeric audience ignored",
			claims: jwt.MapClaims{"aud": 42},
			check: func(t *testing.T, c Claims) {
				assert.Nil(t, c.Audience, "numeric audience should not match any switch case")
			},
		},
		{
			name:   "extra claims collected",
			claims: jwt.MapClaims{"sub": "u1", "custom_field": "val", "nbf": 123.0},
			check: func(t *testing.T, c Claims) {
				assert.Equal(t, "u1", c.Subject)
				assert.Equal(t, "val", c.Extra["custom_field"])
				_, hasNbf := c.Extra["nbf"]
				assert.False(t, hasNbf, "nbf is a standard claim and should not appear in Extra")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mapClaimsToClaims(tt.claims)
			tt.check(t, c)
		})
	}
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
