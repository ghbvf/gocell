// Tests for VerifyIntent issuer validation (AUTH-TRUST-BOUNDARY-160).
//
// Covers the requirement that when WithExpectedIssuer is configured, VerifyIntent
// must enforce that the token's iss claim exactly matches the expected issuer.
// Aligns with coreos/go-oidc v3 IDTokenVerifier issuer validation and
// golang-jwt/jwt v5 WithIssuer ParserOption semantics.
package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTokenWithIss issues a signed access token carrying the given issuer string.
// Pass an empty string to produce a token without an iss claim (via raw MapClaims).
func makeTokenWithIss(t *testing.T, ks *KeySet, iss string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":       "user-1",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
		"aud":       []string{"gocell"},
	}
	if iss != "" {
		claims["iss"] = iss
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)
	return tokenStr
}

// TestJWTVerifier_VerifyIntent_AcceptsMatchingIssuer verifies that a token whose
// iss claim matches WithExpectedIssuer is accepted and claims.Issuer is populated.
func TestJWTVerifier_VerifyIntent_AcceptsMatchingIssuer(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks,
		WithExpectedAudiences("gocell"),
		WithExpectedIssuer("gocell-prod"),
	)
	require.NoError(t, err)

	tok := makeTokenWithIss(t, ks, "gocell-prod")
	claims, err := verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "gocell-prod", claims.Issuer)
}

// TestJWTVerifier_VerifyIntent_RejectsIssuerMismatch verifies that a token whose
// iss claim does not match WithExpectedIssuer is rejected with ErrAuthInvalidTokenIntent.
func TestJWTVerifier_VerifyIntent_RejectsIssuerMismatch(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks,
		WithExpectedAudiences("gocell"),
		WithExpectedIssuer("gocell-prod"),
	)
	require.NoError(t, err)

	tok := makeTokenWithIss(t, ks, "evil-service")
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"issuer mismatch must return ERR_AUTH_INVALID_TOKEN_INTENT")
}

// TestJWTVerifier_VerifyIntent_RejectsMissingIssuer verifies that a token without
// an iss claim is rejected when WithExpectedIssuer is configured.
func TestJWTVerifier_VerifyIntent_RejectsMissingIssuer(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks,
		WithExpectedAudiences("gocell"),
		WithExpectedIssuer("gocell-prod"),
	)
	require.NoError(t, err)

	// makeTokenWithIss with empty string omits the iss claim entirely.
	tok := makeTokenWithIss(t, ks, "")
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"token without iss claim must be rejected when expected issuer is configured")
}

// TestJWTVerifier_VerifyIntent_NoExpectedIssuer_AllowsAnyIssuer verifies that when
// WithExpectedIssuer is not configured, any iss value (including empty) is accepted.
func TestJWTVerifier_VerifyIntent_NoExpectedIssuer_AllowsAnyIssuer(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tests := []struct {
		name string
		iss  string
	}{
		{"any issuer", "some-random-issuer"},
		{"empty issuer omitted", ""},
		{"another service", "other-service"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok := makeTokenWithIss(t, ks, tc.iss)
			_, err := verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
			require.NoError(t, err, "no expected issuer configured — any iss should pass")
		})
	}
}

// TestJWTVerifier_VerifyIntent_IssuerCheckAfterAudience verifies that when both aud
// and iss mismatch, audience error is returned first (aud check precedes iss check).
func TestJWTVerifier_VerifyIntent_IssuerCheckAfterAudience(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks,
		WithExpectedAudiences("gocell"),
		WithExpectedIssuer("gocell-prod"),
	)
	require.NoError(t, err)

	// Build a token with wrong aud AND wrong iss.
	claims := jwt.MapClaims{
		"sub":       "user-1",
		"iss":       "evil-service", // wrong issuer
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
		"aud":       []string{"wrong-aud"}, // wrong audience
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err)
	// Both failures surface ErrAuthInvalidTokenIntent; the message distinguishes "audience"
	// from "issuer". Audience check fires first per placement in VerifyIntent.
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
	assert.Contains(t, err.Error(), "audience",
		"audience check fires before issuer check; error message must mention audience")
}

// TestWithExpectedIssuer_EmptyString_NoOp verifies that WithExpectedIssuer("") is
// equivalent to not calling the option — any issuer is accepted.
func TestWithExpectedIssuer_EmptyString_NoOp(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks,
		WithExpectedAudiences("gocell"),
		WithExpectedIssuer(""), // should be a no-op
	)
	require.NoError(t, err)

	tok := makeTokenWithIss(t, ks, "arbitrary-issuer")
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err, "WithExpectedIssuer(\"\") must be a no-op — any iss accepted")
}
