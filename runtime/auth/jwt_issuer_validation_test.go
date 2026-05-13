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

	"github.com/ghbvf/gocell/kernel/clock"
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
	verifier, err := NewJWTVerifier(ks, clock.Real(),
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
	verifier, err := NewJWTVerifier(ks, clock.Real(),
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
	verifier, err := NewJWTVerifier(ks, clock.Real(),
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
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
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
	verifier, err := NewJWTVerifier(ks, clock.Real(),
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

// TestIssue_JTI_Written verifies that IssueOptions.JTI is written as the "jti"
// JWT claim and appears in the raw token payload.
func TestIssue_JTI_Written(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tokenStr, err := issuer.Issue(TokenIntentAccess, "user-1", IssueOptions{
		Audience:   []string{"gocell"},
		JTI:        "j1",
		AuthzEpoch: 7,
	})
	require.NoError(t, err)

	// Raw payload must contain jti and authz_epoch.
	payload := decodeJWTPayload(t, tokenStr)
	assert.Equal(t, "j1", payload["jti"], "jti claim must be written when JTI is non-empty")
	assert.Equal(t, float64(7), payload["authz_epoch"], "authz_epoch claim must always be written")

	// Claims struct must map jti → JTI and authz_epoch → AuthzEpoch.
	claims, err := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "j1", claims.JTI, "Claims.JTI must be populated from jti claim")
	assert.Equal(t, int64(7), claims.AuthzEpoch, "Claims.AuthzEpoch must be populated from authz_epoch claim")

	// Neither jti nor authz_epoch must appear in Claims.Extra.
	_, jtiInExtra := claims.Extra["jti"]
	assert.False(t, jtiInExtra, "jti must not leak into Claims.Extra")
	_, epochInExtra := claims.Extra["authz_epoch"]
	assert.False(t, epochInExtra, "authz_epoch must not leak into Claims.Extra")
}

// TestIssue_AuthzEpoch_Zero_AlwaysWritten verifies that AuthzEpoch=0 is still
// written into the token payload (0 is a legitimate epoch value, not "absent").
func TestIssue_AuthzEpoch_Zero_AlwaysWritten(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)

	tokenStr, err := issuer.Issue(TokenIntentAccess, "user-1", IssueOptions{
		Audience:   []string{"gocell"},
		AuthzEpoch: 0,
	})
	require.NoError(t, err)

	payload := decodeJWTPayload(t, tokenStr)
	epochVal, hasEpoch := payload["authz_epoch"]
	assert.True(t, hasEpoch, "authz_epoch must always be written even when zero")
	assert.Equal(t, float64(0), epochVal, "authz_epoch must be 0 when IssueOptions.AuthzEpoch is 0")
}

// TestIssue_JTI_Empty_Omitted verifies that when JTI is empty, the jti claim is
// not written into the token payload.
func TestIssue_JTI_Empty_Omitted(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)

	tokenStr, err := issuer.Issue(TokenIntentAccess, "user-1", IssueOptions{
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	payload := decodeJWTPayload(t, tokenStr)
	_, hasJTI := payload["jti"]
	assert.False(t, hasJTI, "jti claim must be absent from token when JTI is empty")
}

// TestWithExpectedIssuer_EmptyString_NoOp verifies that WithExpectedIssuer("") is
// equivalent to not calling the option — any issuer is accepted.
func TestWithExpectedIssuer_EmptyString_NoOp(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, clock.Real(),
		WithExpectedAudiences("gocell"),
		WithExpectedIssuer(""), // should be a no-op
	)
	require.NoError(t, err)

	tok := makeTokenWithIss(t, ks, "arbitrary-issuer")
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err, "WithExpectedIssuer(\"\") must be a no-op — any iss accepted")
}
