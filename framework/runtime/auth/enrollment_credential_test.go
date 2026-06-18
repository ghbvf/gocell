package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

const (
	testEnrollTenant = "11111111-1111-1111-1111-111111111111"
	testEnrollDevice = "device-abc"
	testEnrollAud    = "gocell-est"
)

// newTestEnrollmentScheme builds an issuer + verifier pair over a shared key set,
// issuer string, audience and clock. The enrollment issuer bakes the audience so
// the (audience-required) verifier accepts the credential.
func newTestEnrollmentScheme(t *testing.T, clk clock.Clock) (*EnrollmentCredentialIssuer, *EnrollmentCredentialVerifier) {
	t.Helper()
	ks := mustTestKeySet(t)
	iss, err := NewEnrollmentCredentialIssuer(ks, "gocell", clk, WithIssuerAudiencesFromSlice([]string{testEnrollAud}))
	require.NoError(t, err)
	jwtVer, err := NewJWTVerifier(ks, clk, WithExpectedAudiences(testEnrollAud))
	require.NoError(t, err)
	ver, err := NewEnrollmentCredentialVerifier(jwtVer)
	require.NoError(t, err)
	return iss, ver
}

// signRawEnrollmentJWT forges an enrollment-typed JWT with caller-controlled
// principal_kind / tenant_id, signed by ks at the given clock. Used to exercise
// the verifier's post-VerifyIntent fail-closed assertions.
func signRawEnrollmentJWT(t *testing.T, ks *KeySet, clk clock.Clock, principalKind, tenantID string) string {
	t.Helper()
	now := clk.Now()
	claims := jwt.MapClaims{
		"sub":       testEnrollDevice,
		"iss":       "gocell",
		"aud":       []string{testEnrollAud},
		"iat":       now.Unix(),
		"exp":       now.Add(time.Hour).Unix(),
		"token_use": string(TokenIntentEnrollment),
	}
	if tenantID != "" {
		claims["tenant_id"] = tenantID
	}
	if principalKind != "" {
		claims["principal_kind"] = principalKind
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = jwtTypEnroll
	s, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)
	return s
}

func TestEnrollmentCredentialIssuer_Issue_MintsDeviceScopedToken(t *testing.T) {
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	iss, _ := newTestEnrollmentScheme(t, clockmock.New(fixedNow))

	tok, err := iss.Issue(testEnrollTenant, testEnrollDevice)
	require.NoError(t, err)

	header := decodeJWTHeader(t, tok)
	assert.Equal(t, jwtTypEnroll, header["typ"], "enrollment credential must carry typ=enroll+jwt")

	p := decodeJWTPayload(t, tok)
	assert.Equal(t, string(TokenIntentEnrollment), p["token_use"], "token_use must be enrollment")
	assert.Equal(t, string(PrincipalKindClaimDevice), p["principal_kind"], "enrollment credential is always device-scoped")
	assert.Equal(t, testEnrollTenant, p["tenant_id"], "tenant_id must be carried")
	assert.Equal(t, testEnrollDevice, p["sub"], "sub must be the device subject")
	assert.NotEmpty(t, p["jti"], "enrollment credential must carry a jti (one-time hook)")
	assert.Equal(t, float64(fixedNow.Add(EnrollmentCredentialTTL).Unix()), p["exp"],
		"exp must be now + EnrollmentCredentialTTL (short window), not the access TTL")
}

func TestEnrollmentCredentialIssuer_Issue_FailsClosed(t *testing.T) {
	iss, _ := newTestEnrollmentScheme(t, clock.Real())
	tests := []struct {
		name    string
		tenant  string
		subject string
	}{
		{"empty subject", testEnrollTenant, ""},
		{"empty tenant", "", testEnrollDevice},
		{"both empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := iss.Issue(tc.tenant, tc.subject)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
		})
	}
}

func TestEnrollmentCredentialVerifier_Verify_ReturnsSealedIdentity(t *testing.T) {
	iss, ver := newTestEnrollmentScheme(t, clock.Real())

	tok, err := iss.Issue(testEnrollTenant, testEnrollDevice)
	require.NoError(t, err)

	id, err := ver.Verify(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, testEnrollTenant, id.Tenant())
	assert.Equal(t, testEnrollDevice, id.Subject())
	assert.NotEmpty(t, id.JTI(), "verified identity must surface the jti for the one-time hook")
}

// TestEnrollmentCredentialVerifier_Verify_NonReuseBothDirections proves FR-012's
// "dedicated scheme, not reused": an access token cannot enroll and an enrollment
// credential cannot be used at a business access endpoint.
func TestEnrollmentCredentialVerifier_Verify_NonReuseBothDirections(t *testing.T) {
	ks := mustTestKeySet(t)
	clk := clock.Real()

	enrollIss, err := NewEnrollmentCredentialIssuer(ks, "gocell", clk, WithIssuerAudiencesFromSlice([]string{testEnrollAud}))
	require.NoError(t, err)
	accessIss, err := NewJWTIssuer(ks, "gocell", time.Hour, clk, WithIssuerAudiencesFromSlice([]string{testEnrollAud}))
	require.NoError(t, err)
	jwtVer, err := NewJWTVerifier(ks, clk, WithExpectedAudiences(testEnrollAud))
	require.NoError(t, err)
	enrollVer, err := NewEnrollmentCredentialVerifier(jwtVer)
	require.NoError(t, err)

	accessTok, err := accessIss.Issue(TokenIntentAccess, testEnrollDevice, IssueOptions{TenantID: testEnrollTenant, PrincipalKind: PrincipalKindClaimDevice})
	require.NoError(t, err)
	enrollTok, err := enrollIss.Issue(testEnrollTenant, testEnrollDevice)
	require.NoError(t, err)

	// access token presented to the enrollment verifier → rejected (intent isolation)
	_, err = enrollVer.Verify(context.Background(), accessTok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")

	// enrollment credential presented at a business access endpoint → rejected
	_, err = jwtVer.VerifyIntent(context.Background(), enrollTok, TokenIntentAccess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
}

// TestEnrollmentCredentialVerifier_Verify_RejectsNonDevice forges a well-formed
// enrollment-intent token that is NOT device-scoped (principal_kind absent =
// user). The verifier must fail closed even though VerifyIntent accepts it.
func TestEnrollmentCredentialVerifier_Verify_RejectsNonDevice(t *testing.T) {
	ks := mustTestKeySet(t)
	clk := clock.Real()
	jwtVer, err := NewJWTVerifier(ks, clk, WithExpectedAudiences(testEnrollAud))
	require.NoError(t, err)
	ver, err := NewEnrollmentCredentialVerifier(jwtVer)
	require.NoError(t, err)

	// principal_kind omitted → decodes to user; tenant present and canonical.
	userKindTok := signRawEnrollmentJWT(t, ks, clk, "", testEnrollTenant)
	_, err = ver.Verify(context.Background(), userKindTok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
}

func TestEnrollmentCredentialVerifier_Verify_RejectsExpired(t *testing.T) {
	fixedNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(fixedNow)
	iss, ver := newTestEnrollmentScheme(t, fc)

	tok, err := iss.Issue(testEnrollTenant, testEnrollDevice)
	require.NoError(t, err)

	fc.Advance(EnrollmentCredentialTTL + time.Second)
	_, err = ver.Verify(context.Background(), tok)
	require.Error(t, err, "expired enrollment credential must be rejected")
}

// TestNewEnrollmentIdentity_FailsClosed exercises the sealed constructor directly:
// empty tenant or subject is rejected, independent of the verification path.
func TestNewEnrollmentIdentity_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		tenant  string
		subject string
		wantErr bool
	}{
		{"valid", testEnrollTenant, testEnrollDevice, false},
		{"empty tenant", "", testEnrollDevice, true},
		{"empty subject", testEnrollTenant, "", true},
		{"both empty", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := newEnrollmentIdentity(tc.tenant, tc.subject, "jti-1")
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "ERR_AUTH_UNAUTHORIZED")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.tenant, id.Tenant())
			assert.Equal(t, tc.subject, id.Subject())
			assert.Equal(t, "jti-1", id.JTI())
		})
	}
}

func TestNewEnrollmentCredentialVerifier_RejectsNilVerifier(t *testing.T) {
	_, err := NewEnrollmentCredentialVerifier(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_AUTH_VERIFIER_CONFIG")
}
