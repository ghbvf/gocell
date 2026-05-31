package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	tenantClaimUUID   = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	tenantClaimUUIDUp = "3F2504E0-4F89-41D3-9A0C-0305E82C3301"
)

// TestClaims_TenantID_Parsed verifies the "tenant_id" claim is mapped to
// Claims.TenantID by the real verifier (mirrors TestClaims_JTI_Parsed).
func TestClaims_TenantID_Parsed(t *testing.T) {
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
		"tenant_id": tenantClaimUUID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, tenantClaimUUID, claims.TenantID, "Claims.TenantID must be populated from tenant_id claim")
}

// TestMapClaimsToClaims_TenantID_NotInExtra verifies tenant_id is a standard
// claim and does not leak into Claims.Extra.
func TestMapClaimsToClaims_TenantID_NotInExtra(t *testing.T) {
	mc := jwt.MapClaims{
		"sub":       "u1",
		"tenant_id": tenantClaimUUID,
		"custom":    "val",
	}
	c := mapClaimsToClaims(mc)
	assert.Equal(t, tenantClaimUUID, c.TenantID)
	assert.Equal(t, "val", c.Extra["custom"])
	_, inExtra := c.Extra["tenant_id"]
	assert.False(t, inExtra, "tenant_id must not leak into Claims.Extra")
}

// TestMapClaimsToClaims_TenantID_NonString_Absent verifies a non-string
// tenant_id claim leaves Claims.TenantID empty (treated as absent, like other
// claim mappings — rejection of malformed *values* happens at the authenticator).
func TestMapClaimsToClaims_TenantID_NonString_Absent(t *testing.T) {
	mc := jwt.MapClaims{"sub": "u1", "tenant_id": 12345}
	c := mapClaimsToClaims(mc)
	assert.Equal(t, "", c.TenantID, "non-string tenant_id must not populate Claims.TenantID")
}

// TestJWTAuthenticator_TenantClaim_Valid verifies a valid UUID tenant claim is
// carried onto Principal.TenantID.
func TestJWTAuthenticator_TenantClaim_Valid(t *testing.T) {
	v := &stubVerifier{claims: Claims{
		Subject:  "user-1",
		Issuer:   "gocell",
		TokenUse: TokenIntentAccess,
		TenantID: tenantClaimUUID,
	}}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer t"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, tenantClaimUUID, p.TenantID)
}

// TestJWTAuthenticator_TenantClaim_Canonicalized verifies an uppercase tenant
// claim is normalized to canonical lowercase on the Principal.
func TestJWTAuthenticator_TenantClaim_Canonicalized(t *testing.T) {
	v := &stubVerifier{claims: Claims{
		Subject:  "user-1",
		Issuer:   "gocell",
		TokenUse: TokenIntentAccess,
		TenantID: tenantClaimUUIDUp,
	}}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer t"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, tenantClaimUUID, p.TenantID, "tenant claim must be canonicalized to lowercase UUID")
}

// TestJWTAuthenticator_TenantClaim_Malformed_Rejected verifies a present-but-
// malformed tenant claim fails closed (generic 401), not silently dropped.
func TestJWTAuthenticator_TenantClaim_Malformed_Rejected(t *testing.T) {
	v := &stubVerifier{claims: Claims{
		Subject:  "user-1",
		Issuer:   "gocell",
		TokenUse: TokenIntentAccess,
		TenantID: "not-a-uuid",
	}}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer t"))
	require.Error(t, err)
	assert.False(t, ok)
	assert.Nil(t, p)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code)
}

// TestJWTAuthenticator_TenantClaim_Absent_OK verifies the single-tenant path:
// no tenant claim → no error, empty Principal.TenantID.
func TestJWTAuthenticator_TenantClaim_Absent_OK(t *testing.T) {
	v := &stubVerifier{claims: Claims{
		Subject:  "user-1",
		Issuer:   "gocell",
		TokenUse: TokenIntentAccess,
	}}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer t"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "", p.TenantID)
}

// TestInjectPrincipalCtxKeys_TenantWritten verifies a JWT principal with a
// tenant writes ctxkeys.TenantID for downstream outbox propagation.
func TestInjectPrincipalCtxKeys_TenantWritten(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", TenantID: tenantClaimUUID}
	ctx := injectPrincipalCtxKeys(context.Background(), p)
	got, ok := ctxkeys.TenantIDFrom(ctx)
	require.True(t, ok, "tenant id must be written to ctx")
	assert.Equal(t, tenantClaimUUID, got)
}

// TestInjectPrincipalCtxKeys_NoTenant_NotWritten verifies principals without a
// tenant (service / single-tenant user) do not stamp the key.
func TestInjectPrincipalCtxKeys_NoTenant_NotWritten(t *testing.T) {
	for _, p := range []*Principal{
		{Kind: PrincipalService, CallerCellID: "accesscore"},
		{Kind: PrincipalUser, Subject: "u1"},
	} {
		ctx := injectPrincipalCtxKeys(context.Background(), p)
		_, ok := ctxkeys.TenantIDFrom(ctx)
		assert.False(t, ok, "no tenant key when Principal.TenantID empty (kind=%v)", p.Kind)
	}
}
