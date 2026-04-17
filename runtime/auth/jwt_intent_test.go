// Tests for JWT token intent enforcement (PR-P0-AUTH-INTENT).
//
// Covers:
//   - token_use claim (payload) written by Issue() per TokenIntent
//   - typ header (JOSE) written by Issue() per TokenIntent
//   - VerifyIntent() rejects intent mismatch, missing claim, header/claim divergence
//
// ref: RFC 9068 §2.1 (typ: at+jwt), RFC 8725 §3.11 (token confusion)
// ref: AWS Cognito token_use claim, Keycloak TokenUtil.java typ constants
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeJWTHeader returns the decoded JOSE header map for a token.
func decodeJWTHeader(t *testing.T, tokenStr string) map[string]any {
	t.Helper()
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3)
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var h map[string]any
	require.NoError(t, json.Unmarshal(headerJSON, &h))
	return h
}

// decodeJWTPayload returns the decoded JWT payload as map[string]any.
func decodeJWTPayload(t *testing.T, tokenStr string) map[string]any {
	t.Helper()
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3)
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &p))
	return p
}

func TestJWTIssuer_IssueWithIntent_Access_EmbedsTokenUseClaimAndAtJWTHeader(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue(TokenIntentAccess, "user-1", []string{"admin"}, []string{"gocell"}, "sess-1")
	require.NoError(t, err)

	header := decodeJWTHeader(t, tokenStr)
	assert.Equal(t, "at+jwt", header["typ"], "access token must have typ=at+jwt header (RFC 9068 §2.1)")

	payload := decodeJWTPayload(t, tokenStr)
	assert.Equal(t, "access", payload["token_use"], "access token must have token_use=access claim")
}

func TestJWTIssuer_IssueWithIntent_Refresh_EmbedsTokenUseClaimAndRefreshHeader(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue(TokenIntentRefresh, "user-1", nil, []string{"gocell"}, "sess-1")
	require.NoError(t, err)

	header := decodeJWTHeader(t, tokenStr)
	assert.Equal(t, "refresh+jwt", header["typ"], "refresh token must have typ=refresh+jwt header")

	payload := decodeJWTPayload(t, tokenStr)
	assert.Equal(t, "refresh", payload["token_use"], "refresh token must have token_use=refresh claim")
}

func TestJWTIssuer_IssueWithIntent_InvalidIntent_Rejected(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)

	_, err = issuer.Issue(TokenIntent("bogus"), "user-1", nil, nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
}

func TestJWTVerifier_Verify_PopulatesTokenUseOnClaims(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	tokenStr, err := issuer.Issue(TokenIntentAccess, "user-1", nil, nil, "")
	require.NoError(t, err)

	claims, err := verifier.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, TokenIntentAccess, claims.TokenUse)
}

func TestJWTVerifier_VerifyIntent_AcceptsMatchingIntent(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	access, err := issuer.Issue(TokenIntentAccess, "user-1", []string{"admin"}, nil, "sid-1")
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), access, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, TokenIntentAccess, claims.TokenUse)

	refresh, err := issuer.Issue(TokenIntentRefresh, "user-1", nil, nil, "sid-1")
	require.NoError(t, err)
	rc, err := verifier.VerifyIntent(context.Background(), refresh, TokenIntentRefresh)
	require.NoError(t, err)
	assert.Equal(t, TokenIntentRefresh, rc.TokenUse)
}

func TestJWTVerifier_VerifyIntent_RejectsWrongIntent_Access(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	refresh, err := issuer.Issue(TokenIntentRefresh, "user-1", nil, nil, "")
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), refresh, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
}

func TestJWTVerifier_VerifyIntent_RejectsWrongIntent_Refresh(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	access, err := issuer.Issue(TokenIntentAccess, "user-1", nil, nil, "")
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), access, TokenIntentRefresh)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
}

func TestJWTVerifier_VerifyIntent_RejectsMissingTokenUseClaim(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	// Manually forge a legacy-style token: valid signature + kid but no token_use claim.
	claims := jwt.MapClaims{
		"sub": "user-legacy",
		"iss": "gocell",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tokenStr, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"fail-closed: token without token_use claim must be rejected")
}

func TestJWTVerifier_VerifyIntent_RejectsHeaderClaimMismatch(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	// Forge a token where typ header says "at+jwt" but token_use claim says "refresh".
	// VerifyIntent must reject this chimera regardless of expected intent.
	claims := jwt.MapClaims{
		"sub":       "attacker",
		"iss":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "refresh",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = "at+jwt"
	tokenStr, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err)
	_, err2 := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentRefresh)
	require.Error(t, err2)
}

func TestJWTVerifier_VerifyIntent_RejectsUnknownIntentArg(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	tok, err := issuer.Issue(TokenIntentAccess, "user-1", nil, nil, "")
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tok, TokenIntent("bogus"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
}

// TestJWTIssuer_IssueWithIntent_RefreshUsesRefreshTTL verifies that Issue()
// uses DefaultRefreshTokenTTL for refresh tokens and the access ttl for access
// tokens. Uses a fixed clock to compare exp precisely.
func TestJWTIssuer_IssueWithIntent_RefreshUsesRefreshTTL(t *testing.T) {
	ks := mustTestKeySet(t)
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issuer, err := NewJWTIssuer(ks, "gocell", DefaultAccessTokenTTL, WithIssuerClock(func() time.Time { return fixedNow }))
	require.NoError(t, err)

	accessStr, err := issuer.Issue(TokenIntentAccess, "user-1", nil, nil, "")
	require.NoError(t, err)
	accessPayload := decodeJWTPayload(t, accessStr)
	accessExp := int64(accessPayload["exp"].(float64))
	assert.Equal(t, fixedNow.Add(DefaultAccessTokenTTL).Unix(), accessExp,
		"access token exp must be now+DefaultAccessTokenTTL (15min)")

	refreshStr, err := issuer.Issue(TokenIntentRefresh, "user-1", nil, nil, "")
	require.NoError(t, err)
	refreshPayload := decodeJWTPayload(t, refreshStr)
	refreshExp := int64(refreshPayload["exp"].(float64))
	assert.Equal(t, fixedNow.Add(DefaultRefreshTokenTTL).Unix(), refreshExp,
		"refresh token exp must be now+DefaultRefreshTokenTTL (7 days)")

	assert.Greater(t, refreshExp, accessExp,
		"refresh token must live longer than access token")
}

// TestJWTVerifier_VerifyIntent_RejectsLegacyTypHeader verifies that a token
// carrying typ="JWT" (RFC 7519 legacy plain JWT) is rejected even when the
// token_use claim is valid. intentForJWTTyp("JWT") returns ("", false),
// triggering the "typ header missing or unknown" fail-closed path. This closes
// the chimera vector where a pre-intent legacy issuer signs typ=JWT tokens that
// carry a plausible token_use claim.
func TestJWTVerifier_VerifyIntent_RejectsLegacyTypHeader(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	// RFC 7519 legacy: typ="JWT" (upper-case) + token_use=access
	claims := jwt.MapClaims{
		"sub":       "user-legacy",
		"iss":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = "JWT" // RFC 7519 plain JWT, not at+jwt
	tokenStr, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"typ=JWT (RFC 7519 legacy) must be rejected — intentForJWTTyp returns false")
}

// TestJWTVerifier_VerifyIntent_RejectsMissingTypHeader verifies that a token
// without any typ header at all is rejected even when token_use claim is valid.
func TestJWTVerifier_VerifyIntent_RejectsMissingTypHeader(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	claims := jwt.MapClaims{
		"sub":       "user-1",
		"iss":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	// Deliberately omit typ header — golang-jwt/jwt sets typ="JWT" by default
	// in the header map, so delete it explicitly.
	delete(tok.Header, "typ")
	tokenStr, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"missing typ header must be rejected")
}

// TestJWTVerifier_VerifyIntent_RejectsNonStringTypHeader verifies that a token
// where typ is a non-string value (e.g. integer 42) does not panic and is
// rejected. stringFromHeader uses a type-assertion that returns "" on failure,
// which leads intentForJWTTyp to return ("", false).
func TestJWTVerifier_VerifyIntent_RejectsNonStringTypHeader(t *testing.T) {
	priv, pub := generateTestKeyPair(t)
	ks, err := NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks)
	require.NoError(t, err)

	claims := jwt.MapClaims{
		"sub":       "attacker",
		"iss":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = 42 // non-string — stringFromHeader returns ""
	tokenStr, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"non-string typ header must be rejected and must not panic")
}

// Compile-time check: *JWTVerifier satisfies IntentTokenVerifier.
var _ IntentTokenVerifier = (*JWTVerifier)(nil)
