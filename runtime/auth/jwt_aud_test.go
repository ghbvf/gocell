// Tests for VerifyIntent audience validation (PR-R-AUTH-AUD-VALIDATION).
//
// Covers RFC 8725 §3.3: "recipients MUST validate the aud claim to determine
// that the JWT is indeed intended for the recipient."
//
// WithExpectedAudiences configures the verifier; when not set the check is
// skipped (backward compatible). When set, at least one configured audience
// must appear in the token's aud claim.
package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTokenWithAud issues a signed token carrying the given audience slice.
// Pass nil to produce a token without an aud claim.
func makeTokenWithAud(t *testing.T, ks *KeySet, aud []string) string {
	t.Helper()
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	tok, err := issuer.Issue(TokenIntentAccess, "user-1", nil, aud, "")
	require.NoError(t, err)
	return tok
}

// makeRawTokenWithAud builds a token manually so we can omit the aud claim entirely.
func makeRawTokenWithoutAud(t *testing.T, ks *KeySet) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":       "user-1",
		"iss":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)
	return tokenStr
}

// TestJWTVerifier_VerifyIntent_AcceptsMatchingAudience verifies that a token
// whose aud claim contains the configured expected audience is accepted.
func TestJWTVerifier_VerifyIntent_AcceptsMatchingAudience(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tok := makeTokenWithAud(t, ks, []string{"gocell"})
	claims, err := verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
}

// TestJWTVerifier_VerifyIntent_RejectsAudienceMismatch verifies that a token
// whose aud claim does not contain the expected audience is rejected with
// ERR_AUTH_UNAUTHORIZED (enumeration defense: same code as other 401 errors).
func TestJWTVerifier_VerifyIntent_RejectsAudienceMismatch(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tok := makeTokenWithAud(t, ks, []string{"other-service"})
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED",
		"audience mismatch must return ERR_AUTH_UNAUTHORIZED (enumeration defense)")
}

// TestJWTVerifier_VerifyIntent_RejectsMissingAudience verifies that a token
// with no aud claim at all is rejected when an expected audience is configured.
func TestJWTVerifier_VerifyIntent_RejectsMissingAudience(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tok := makeRawTokenWithoutAud(t, ks)
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED",
		"token without aud claim must be rejected when expected audience is configured")
}

// TestJWTVerifier_VerifyIntent_AudienceCheckSkippedWhenNotConfigured verifies
// backward compatibility: when WithExpectedAudiences is not called, VerifyIntent
// skips the audience check and accepts tokens regardless of aud content.
func TestJWTVerifier_VerifyIntent_AudienceCheckSkippedWhenNotConfigured(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks) // no WithExpectedAudiences
	require.NoError(t, err)

	// Token with mismatched audience — should still pass when no expectation configured.
	tok := makeTokenWithAud(t, ks, []string{"some-other-audience"})
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err, "no expected audience configured → aud check skipped (backward compat)")
}

// TestJWTVerifier_VerifyIntent_NoAudSkippedWhenNotConfigured mirrors the above
// but with a token that has no aud claim at all.
func TestJWTVerifier_VerifyIntent_NoAudSkippedWhenNotConfigured(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks) // no WithExpectedAudiences
	require.NoError(t, err)

	tok := makeRawTokenWithoutAud(t, ks)
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err, "no expected audience configured → aud check skipped")
}

// TestJWTVerifier_VerifyIntent_AcceptsMultipleAudiencesWhenOneMatches verifies
// RFC 7519 §4.1.3 semantics: when the token's aud is a multi-element array,
// it is sufficient for one element to match the expected audience.
func TestJWTVerifier_VerifyIntent_AcceptsMultipleAudiencesWhenOneMatches(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tok := makeTokenWithAud(t, ks, []string{"api-gateway", "gocell", "metrics"})
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err, "one of the token audiences matching the expected audience is sufficient")
}

// TestJWTVerifier_VerifyIntent_AcceptsWhenOneOfMultipleExpectedMatches verifies
// that when multiple expected audiences are configured via WithExpectedAudiences,
// a token matching any one of them is accepted.
func TestJWTVerifier_VerifyIntent_AcceptsWhenOneOfMultipleExpectedMatches(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell", "api-gateway"))
	require.NoError(t, err)

	tok := makeTokenWithAud(t, ks, []string{"gocell"})
	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntentAccess)
	require.NoError(t, err)

	tok2 := makeTokenWithAud(t, ks, []string{"api-gateway"})
	_, err = verifier.VerifyIntent(context.Background(), tok2, TokenIntentAccess)
	require.NoError(t, err)
}

// TestJWTVerifier_VerifyIntent_AudienceCheckAppliedAfterIntentCheck confirms the
// check ordering: intent validation happens before audience validation, so a wrong-intent
// token returns ErrAuthInvalidTokenIntent even when the audience would also fail.
func TestJWTVerifier_VerifyIntent_AudienceCheckAppliedAfterIntentCheck(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// Refresh token with wrong audience: intent check fires first.
	refreshTok, err := issuer.Issue(TokenIntentRefresh, "user-1", nil, []string{"wrong"}, "")
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), refreshTok, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"intent check fires before audience check")
}

// TestJWTVerifier_Verify_UnaffectedByExpectedAudiences verifies that the plain
// Verify() method is NOT affected by WithExpectedAudiences — audience validation
// is intentionally scoped to VerifyIntent only.
func TestJWTVerifier_Verify_UnaffectedByExpectedAudiences(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// Token with mismatched aud — Verify should not check it.
	tok := makeTokenWithAud(t, ks, []string{"some-other-service"})
	_, err = verifier.Verify(context.Background(), tok)
	require.NoError(t, err, "Verify() must not enforce audience (only VerifyIntent does)")
}
