package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// signAccessTokenWithTenant signs an access JWT carrying the given tenant_id
// claim value (empty omits the claim). Mirrors the inline signing in
// claims_test.go; used to exercise the REAL verifier (where tenant validation
// is armed), not the stub.
func signAccessTokenWithTenant(t *testing.T, ks *KeySet, tenantID string) string {
	t.Helper()
	raw := jwt.MapClaims{
		"sub":       "user-1",
		"iss":       "gocell",
		"aud":       "gocell",
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": string(TokenIntentAccess),
	}
	if tenantID != "" {
		raw["tenant_id"] = tenantID
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	s, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)
	return s
}

func newTenantTestVerifier(t *testing.T) (IntentTokenVerifier, *KeySet) {
	t.Helper()
	ks := mustTestKeySet(t)
	v, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	return v, ks
}

// --- mapping (pure decode) ---

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
// tenant_id claim leaves Claims.TenantID empty (treated as absent at the pure
// mapping layer; the verifier never sees a value to reject).
func TestMapClaimsToClaims_TenantID_NonString_Absent(t *testing.T) {
	mc := jwt.MapClaims{"sub": "u1", "tenant_id": 12345}
	c := mapClaimsToClaims(mc)
	assert.Equal(t, "", c.TenantID, "non-string tenant_id must not populate Claims.TenantID")
}

// --- verifier-level validation (the unbypassable chokepoint) ---

// TestVerifyIntent_TenantClaim_Valid verifies a valid UUID tenant claim is
// carried through the real verifier.
func TestVerifyIntent_TenantClaim_Valid(t *testing.T) {
	v, ks := newTenantTestVerifier(t)
	claims, err := v.VerifyIntent(context.Background(), signAccessTokenWithTenant(t, ks, tenantClaimUUID), TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, tenantClaimUUID, claims.TenantID)
}

// TestVerifyIntent_TenantClaim_Canonicalized verifies the verifier normalizes
// an uppercase tenant claim to canonical lowercase.
func TestVerifyIntent_TenantClaim_Canonicalized(t *testing.T) {
	v, ks := newTenantTestVerifier(t)
	claims, err := v.VerifyIntent(context.Background(), signAccessTokenWithTenant(t, ks, tenantClaimUUIDUp), TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, tenantClaimUUID, claims.TenantID, "verifier must canonicalize tenant claim to lowercase UUID")
}

// TestVerifyIntent_TenantClaim_Malformed_Rejected verifies a present-but-
// malformed tenant claim fails closed at the verifier (generic 401 envelope),
// arming EVERY downstream JWT→Principal path.
func TestVerifyIntent_TenantClaim_Malformed_Rejected(t *testing.T) {
	v, ks := newTenantTestVerifier(t)
	_, err := v.VerifyIntent(context.Background(), signAccessTokenWithTenant(t, ks, "not-a-uuid"), TokenIntentAccess)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code)
}

// TestVerifyIntent_TenantClaim_Absent_OK verifies the single-tenant path: no
// tenant claim → no error, empty TenantID.
func TestVerifyIntent_TenantClaim_Absent_OK(t *testing.T) {
	v, ks := newTenantTestVerifier(t)
	claims, err := v.VerifyIntent(context.Background(), signAccessTokenWithTenant(t, ks, ""), TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "", claims.TenantID)
}

// --- production HTTP path (AuthMiddleware → handleAuthRequest) ---

// TestAuthMiddleware_MalformedTenant_Returns401 proves the production HTTP path
// is armed: a malformed tenant_id JWT claim is rejected with 401 BEFORE the
// handler runs. Uses the REAL verifier (validation lives there); the handler
// must never be reached.
func TestAuthMiddleware_MalformedTenant_Returns401(t *testing.T) {
	v, ks := newTenantTestVerifier(t)
	reached := false
	handler := AuthMiddleware(clock.Real(), v)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer "+signAccessTokenWithTenant(t, ks, "not-a-uuid"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "malformed tenant_id must be rejected on the production HTTP path")
	assert.False(t, reached, "handler must not run when the tenant claim is malformed")
}

// TestAuthMiddleware_ValidTenant_InjectsCtxKey proves a valid tenant claim on
// the production HTTP path is canonicalized and propagated to ctxkeys.TenantID
// for the downstream outbox principal envelope.
func TestAuthMiddleware_ValidTenant_InjectsCtxKey(t *testing.T) {
	v, ks := newTenantTestVerifier(t)
	var gotTenant string
	var tenantOK bool
	handler := AuthMiddleware(clock.Real(), v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, tenantOK = ctxkeys.TenantIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer "+signAccessTokenWithTenant(t, ks, tenantClaimUUIDUp))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, tenantOK, "tenant id must be written to ctx on the HTTP path")
	assert.Equal(t, tenantClaimUUID, gotTenant, "ctx tenant must be the canonical lowercase UUID")
}

// --- injectPrincipalCtxKeys unit ---

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
// tenant (service / single-tenant user) do not stamp the key, and asserts a
// service principal carries no tenant (callerCellID is not a tenant).
func TestInjectPrincipalCtxKeys_NoTenant_NotWritten(t *testing.T) {
	svc := &Principal{Kind: PrincipalService, CallerCellID: "accesscore"}
	assert.Empty(t, svc.TenantID, "service principals must not carry a tenant (callerCellID is not a tenant)")
	for _, p := range []*Principal{
		svc,
		{Kind: PrincipalUser, Subject: "u1"},
	} {
		ctx := injectPrincipalCtxKeys(context.Background(), p)
		_, ok := ctxkeys.TenantIDFrom(ctx)
		assert.False(t, ok, "no tenant key when Principal.TenantID empty (kind=%v)", p.Kind)
	}
}
