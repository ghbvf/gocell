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
func TestClaims_JTI_Parsed(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	raw := jwt.MapClaims{
		"sub":         "user-1",
		"iss":         "gocell",
		"aud":         "gocell",
		"exp":         time.Now().Add(time.Hour).Unix(),
		"iat":         time.Now().Unix(),
		"token_use":   string(TokenIntentAccess),
		"jti":         "j1",
		"authz_epoch": float64(7),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "j1", claims.JTI, "Claims.JTI must be populated from the jti claim")
	assert.Equal(t, int64(7), claims.AuthzEpoch, "Claims.AuthzEpoch must be populated from authz_epoch claim")
}

// TestClaims_AuthzEpoch_Zero verifies that authz_epoch=0 maps to Claims.AuthzEpoch=0
// (zero is a valid epoch value, not absence).
func TestClaims_AuthzEpoch_Zero(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	raw := jwt.MapClaims{
		"sub":         "user-1",
		"aud":         "gocell",
		"exp":         time.Now().Add(time.Hour).Unix(),
		"iat":         time.Now().Unix(),
		"token_use":   string(TokenIntentAccess),
		"authz_epoch": float64(0),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, int64(0), claims.AuthzEpoch, "AuthzEpoch=0 must be preserved, not treated as absent")
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
	assert.Equal(t, int64(0), claims.AuthzEpoch, "Claims.AuthzEpoch must be 0 when authz_epoch claim is absent")
}

// TestMapClaimsToClaims_JTI_AuthzEpoch_NotInExtra verifies that jti and
// authz_epoch are standard claims and must not appear in Claims.Extra.
func TestMapClaimsToClaims_JTI_AuthzEpoch_NotInExtra(t *testing.T) {
	mc := jwt.MapClaims{
		"sub":         "u1",
		"jti":         "abc",
		"authz_epoch": float64(42),
		"custom":      "val",
	}
	c := mapClaimsToClaims(mc)
	assert.Equal(t, "abc", c.JTI)
	assert.Equal(t, int64(42), c.AuthzEpoch)
	assert.Equal(t, "val", c.Extra["custom"])
	_, jtiInExtra := c.Extra["jti"]
	assert.False(t, jtiInExtra, "jti must not leak into Claims.Extra")
	_, epochInExtra := c.Extra["authz_epoch"]
	assert.False(t, epochInExtra, "authz_epoch must not leak into Claims.Extra")
}

// TestMapClaimsToClaims_AuthzEpoch_TypeVariants verifies that authz_epoch is
// parsed correctly from float64, int64, and json.Number forms (all legal JSON
// numeric representations).
func TestMapClaimsToClaims_AuthzEpoch_TypeVariants(t *testing.T) {
	tests := []struct {
		name     string
		rawValue any
		want     int64
	}{
		{"float64", float64(99), 99},
		{"int64", int64(42), 42},
		{"zero float64", float64(0), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := jwt.MapClaims{
				"authz_epoch": tc.rawValue,
			}
			c := mapClaimsToClaims(mc)
			assert.Equal(t, tc.want, c.AuthzEpoch)
		})
	}
}
