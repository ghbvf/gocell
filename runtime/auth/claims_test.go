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

// TestClaims_JTI_Parsed verifies that the "jti" JWT claim is mapped to Claims.JTI.
// S4d: authz_epoch claim removed from JWT; epoch provenance is now stored on
// session/refresh rows. Legacy tokens that happen to include an authz_epoch
// claim have the value ignored by mapClaimsToClaims.
func TestClaims_JTI_Parsed(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	raw := jwt.MapClaims{
		"sub":       "user-1",
		"iss":       "gocell",
		"aud":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": string(TokenIntentAccess),
		"jti":       "j1",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "j1", claims.JTI, "Claims.JTI must be populated from the jti claim")
}

// TestClaims_JTI_Absent_Zero verifies that when the jti claim is absent,
// Claims.JTI is the empty string (zero value).
func TestClaims_JTI_Absent_Zero(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	raw := jwt.MapClaims{
		"sub":       "user-1",
		"aud":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": string(TokenIntentAccess),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "", claims.JTI, "Claims.JTI must be empty string when jti claim is absent")
}

// TestMapClaimsToClaims_JTI_NotInExtra verifies that jti is a standard claim
// and must not appear in Claims.Extra.
// S4d: authz_epoch no longer a standard claim; if present in a legacy token it
// flows into Extra (no special handling).
func TestMapClaimsToClaims_JTI_NotInExtra(t *testing.T) {
	mc := jwt.MapClaims{
		"sub":    "u1",
		"jti":    "abc",
		"custom": "val",
	}
	c := mapClaimsToClaims(mc)
	assert.Equal(t, "abc", c.JTI)
	assert.Equal(t, "val", c.Extra["custom"])
	_, jtiInExtra := c.Extra["jti"]
	assert.False(t, jtiInExtra, "jti must not leak into Claims.Extra")
}
