// Tests for VerifyIntent audience validation (PR-R-AUTH-AUD-VALIDATION) and
// JWTIssuer default audience configuration (AUTH-TRUST-BOUNDARY-160).
//
// Covers RFC 8725 §3.3: "recipients MUST validate the aud claim to determine
// that the JWT is indeed intended for the recipient."
//
// WithExpectedAudiences is required — NewJWTVerifier returns an error when no
// expected audiences are configured (fail-fast per RFC 8725 §3.3). At least
// one configured audience must appear in the token's aud claim.
//
// Shape: 3 Test* funcs — TestJWTVerifier_VerifyIntent_AudienceTable (8 rows),
// TestJWTIssuer_DefaultAudience_Table (3 rows),
// TestNewJWTVerifier_NoAudiences_ReturnsError (standalone).
package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// makeTokenWithAud issues a signed token carrying the given audience slice.
// Pass nil to produce a token without an aud claim.
func makeTokenWithAud(t *testing.T, ks *KeySet, aud []string) string {
	t.Helper()
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour)
	require.NoError(t, err)
	tok, err := issuer.Issue(TokenIntentAccess, "user-1", IssueOptions{Audience: aud})
	require.NoError(t, err)
	return tok
}

// makeRawTokenWithoutAud builds a token manually so we can omit the aud claim entirely.
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

// signTokenWithRawAud builds a token with an arbitrary aud claim value (any type).
// Used for non-standard aud types such as plain string or integer.
func signTokenWithRawAud(t *testing.T, ks *KeySet, audClaim any) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":       "user-1",
		"iss":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": "access",
		"aud":       audClaim,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)
	return tokenStr
}

// verifierAudCase is a single row in TestJWTVerifier_VerifyIntent_AudienceTable.
type verifierAudCase struct {
	name             string
	expectedAuds     []string                              // verifier config
	tokenAud         any                                   // []string=array, string=single, int=invalid type, nil=omit claim
	mintFn           func(t *testing.T, ks *KeySet) string // non-nil overrides tokenAud dispatch
	wantErrSubstring string                                // empty = expect no error
}

// mintTokenForCase dispatches token minting based on case fields.
func mintTokenForCase(t *testing.T, ks *KeySet, tc verifierAudCase) string {
	t.Helper()
	if tc.mintFn != nil {
		return tc.mintFn(t, ks)
	}
	if tc.tokenAud == nil {
		return makeRawTokenWithoutAud(t, ks)
	}
	if aud, ok := tc.tokenAud.([]string); ok {
		return makeTokenWithAud(t, ks, aud)
	}
	// string or int — use raw aud claim
	return signTokenWithRawAud(t, ks, tc.tokenAud)
}

// TestJWTVerifier_VerifyIntent_AudienceTable covers all audience-validation
// paths through VerifyIntent (RFC 8725 §3.3). Eight scenarios; access-path
// historical duplicate dropped (Verify no longer exists — compile-time guarantee).
func TestJWTVerifier_VerifyIntent_AudienceTable(t *testing.T) {
	cases := []verifierAudCase{
		{
			name:         "accepts_matching_audience",
			expectedAuds: []string{"gocell"},
			tokenAud:     []string{"gocell"},
		},
		{
			name:             "rejects_audience_mismatch",
			expectedAuds:     []string{"gocell"},
			tokenAud:         []string{"other-service"},
			wantErrSubstring: "ERR_AUTH_INVALID_TOKEN_INTENT",
		},
		{
			name:             "rejects_missing_audience",
			expectedAuds:     []string{"gocell"},
			tokenAud:         nil,
			wantErrSubstring: "ERR_AUTH_INVALID_TOKEN_INTENT",
		},
		{
			name:         "accepts_multiple_audiences_when_one_matches",
			expectedAuds: []string{"gocell"},
			tokenAud:     []string{"api-gateway", "gocell", "metrics"},
		},
		{
			name:         "accepts_when_one_of_multiple_expected_matches",
			expectedAuds: []string{"gocell", "api-gateway"},
			tokenAud:     []string{"gocell"},
		},
		{
			// Intent check fires before audience check: refresh-shaped token verified
			// as access intent yields ERR_AUTH_INVALID_TOKEN_INTENT even though aud
			// would also fail.
			name:         "audience_check_after_intent_check",
			expectedAuds: []string{"gocell"},
			mintFn: func(t *testing.T, ks *KeySet) string {
				return signRawIntentJWT(t, ks, "refresh", "refresh+jwt", []string{"wrong"})
			},
			wantErrSubstring: "ERR_AUTH_INVALID_TOKEN_INTENT",
		},
		{
			// RFC 7519 §4.1.3: aud may be a single JSON string; parseAudience normalises it.
			name:         "accepts_single_string_aud",
			expectedAuds: []string{"gocell"},
			tokenAud:     "gocell",
		},
		{
			// Non-standard aud type (integer) must be rejected without panicking.
			name:             "rejects_non_string_type_aud",
			expectedAuds:     []string{"gocell"},
			tokenAud:         123,
			wantErrSubstring: "ERR_AUTH_INVALID_TOKEN_INTENT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ks := mustTestKeySet(t)
			require.NotEmpty(t, tc.expectedAuds, "test case must declare expectedAuds")
			verifier, err := NewJWTVerifier(ks,
				WithExpectedAudiences(tc.expectedAuds[0], tc.expectedAuds[1:]...))
			require.NoError(t, err)

			token := mintTokenForCase(t, ks, tc)
			claims, err := verifier.VerifyIntent(context.Background(), token, TokenIntentAccess)

			if tc.wantErrSubstring == "" {
				require.NoError(t, err)
				assert.Equal(t, "user-1", claims.Subject)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSubstring)
			}
		})
	}
}

// TestNewJWTVerifier_NoAudiences_ReturnsError verifies that NewJWTVerifier fails
// at construction time when no expected audiences are configured (RFC 8725 §3.3
// fail-fast). Any composition root that forgets WithExpectedAudiences will get a
// hard error instead of silently skipping audience validation.
func TestNewJWTVerifier_NoAudiences_ReturnsError(t *testing.T) {
	ks := mustTestKeySet(t)
	_, err := NewJWTVerifier(ks)
	require.Error(t, err, "NewJWTVerifier without WithExpectedAudiences must return an error")
	assert.Contains(t, err.Error(), "audience")
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must be errcode.Error")
	assert.Equal(t, errcode.ErrAuthVerifierConfig, ecErr.Code,
		"construction error must use ErrAuthVerifierConfig, not ErrAuthKeyInvalid")
}

// --- WithDefaultAudience + DefaultAudience accessor tests (AUTH-TRUST-BOUNDARY-160) ---

// decodeTokenAudience parses a signed JWT and returns its aud claim as []string.
func decodeTokenAudience(t *testing.T, tokenStr string) []string {
	t.Helper()
	// jwt.ParseUnsecured is not available; use ParseWithClaims with no validation.
	tok, _, err := new(jwt.Parser).ParseUnverified(tokenStr, jwt.MapClaims{})
	require.NoError(t, err)
	mc, ok := tok.Claims.(jwt.MapClaims)
	require.True(t, ok)
	return parseAudience(mc["aud"])
}

// issuerAudCase is a single row in TestJWTIssuer_DefaultAudience_Table.
type issuerAudCase struct {
	name         string
	issuerAuds   []string // WithIssuerAudiencesFromSlice
	optsAud      []string // IssueOptions.Audience (nil = use default)
	wantTokenAud []string
}

// TestJWTIssuer_DefaultAudience_Table covers the three issuer-audience
// configuration paths (AUTH-TRUST-BOUNDARY-160).
func TestJWTIssuer_DefaultAudience_Table(t *testing.T) {
	cases := []issuerAudCase{
		{
			name:         "default_used_when_opts_empty",
			issuerAuds:   []string{"gocell"},
			optsAud:      nil,
			wantTokenAud: []string{"gocell"},
		},
		{
			name:         "opts_overrides_default",
			issuerAuds:   []string{"gocell"},
			optsAud:      []string{"other"},
			wantTokenAud: []string{"other"},
		},
		{
			name:         "multiple_default_audiences_all_written",
			issuerAuds:   []string{"gocell", "api-gateway"},
			optsAud:      nil,
			wantTokenAud: []string{"gocell", "api-gateway"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ks := mustTestKeySet(t)
			issuer, err := NewJWTIssuer(ks, "gocell", time.Hour,
				WithIssuerAudiencesFromSlice(tc.issuerAuds),
			)
			require.NoError(t, err)

			tok, err := issuer.Issue(TokenIntentAccess, "user-1", IssueOptions{Audience: tc.optsAud})
			require.NoError(t, err)

			aud := decodeTokenAudience(t, tok)
			assert.Equal(t, tc.wantTokenAud, aud)
		})
	}
}
