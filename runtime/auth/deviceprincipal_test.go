package auth

// Tests for the production device-principal issuer (#1898, epic #1895 PR-2):
// device token → PrincipalDevice, sealed construction (DEVICE-PRINCIPAL-MINT-CALLER-01),
// fail-closed validation, and concept isolation (service ≠ device).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// mustMintDevice mints a sealed device principal through the sanctioned issuer
// for tests that need a valid PrincipalDevice (a bare Principal{Kind:
// PrincipalDevice} literal lacks the seal and is rejected by RowVisibility).
func mustMintDevice(t *testing.T, subject string) *Principal {
	t.Helper()
	p, err := mintDevicePrincipal(Claims{
		Subject:       subject,
		TenantID:      deviceTestTenant,
		PrincipalKind: PrincipalKindClaimDevice,
	})
	require.NoError(t, err)
	return p
}

// deviceTestTenant is a canonical (non-nil) lowercase UUID accepted by
// pkg/tenant.ParseTenantID. The nil UUID is illegal per tenancy rules.
const deviceTestTenant = "11111111-1111-1111-1111-111111111111"

// --- unit: mintDevicePrincipal (the sole sanctioned PrincipalDevice producer) ---

// TestMintDevicePrincipal_NonPrivilegedRolesAccepted proves that
// hasPrivilegedRole only rejects admin/superadmin — a device token carrying a
// non-privileged role (e.g. "viewer") or an empty non-nil Roles slice is
// accepted by mintDevicePrincipal. This bounds the semantics of the role check
// so a future maintainer cannot mistake it for "any role is forbidden".
func TestMintDevicePrincipal_NonPrivilegedRolesAccepted(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
	}{
		{"viewer_role_accepted", []string{"viewer"}},
		{"empty_non_nil_roles_accepted", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := mintDevicePrincipal(Claims{
				Subject:       "device-1",
				TenantID:      deviceTestTenant,
				Roles:         tc.roles,
				PrincipalKind: PrincipalKindClaimDevice,
			})
			require.NoError(t, err, "device mint must succeed for non-privileged roles")
			assert.Equal(t, PrincipalDevice, p.Kind)
		})
	}
}

func TestMintDevicePrincipal_Valid(t *testing.T) {
	p, err := mintDevicePrincipal(Claims{
		Subject:       "device-42",
		TenantID:      deviceTestTenant,
		PrincipalKind: PrincipalKindClaimDevice,
		ExpiresAt:     time.Unix(1_000_000, 0),
	})
	require.NoError(t, err)
	assert.Equal(t, PrincipalDevice, p.Kind)
	assert.Equal(t, "device-42", p.Subject)
	assert.Equal(t, deviceTestTenant, p.TenantID)
	assert.Empty(t, p.Roles, "device principal must not carry roles (concept isolation)")
	assert.Empty(t, p.Claims["sid"], "device principal carries no session baggage")
	assert.False(t, p.PasswordResetRequired, "device principal carries no password-reset baggage")

	// The seal is what makes RowVisibility grant RowScopeDevice. A device
	// principal minted here must derive RowScopeDevice with its subject.
	vis, err := p.RowVisibility(context.Background())
	require.NoError(t, err)
	assert.Equal(t, tenant.RowScopeDevice, vis.Scope())
	assert.Equal(t, "device-42", vis.Subject())
}

func TestMintDevicePrincipal_FailClosed(t *testing.T) {
	cases := []struct {
		name   string
		claims Claims
	}{
		{
			name:   "missing subject",
			claims: Claims{TenantID: deviceTestTenant, PrincipalKind: PrincipalKindClaimDevice},
		},
		{
			name:   "missing tenant",
			claims: Claims{Subject: "device-1", PrincipalKind: PrincipalKindClaimDevice},
		},
		{
			name: "carries admin role",
			claims: Claims{
				Subject: "device-1", TenantID: deviceTestTenant,
				Roles: []string{RoleAdmin}, PrincipalKind: PrincipalKindClaimDevice,
			},
		},
		{
			name: "carries superadmin role",
			claims: Claims{
				Subject: "device-1", TenantID: deviceTestTenant,
				Roles: []string{RoleSuperAdmin}, PrincipalKind: PrincipalKindClaimDevice,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := mintDevicePrincipal(tc.claims)
			require.Error(t, err, "device mint must fail closed")
			assert.Nil(t, p)
		})
	}
}

// --- Hard seal proof: a forged Principal{Kind: PrincipalDevice} (no seal) is
// type-inert — RowVisibility refuses to derive RowScopeDevice from it. ---

func TestDevicePrincipal_ForgedWithoutSeal_RowVisibilityFailClosed(t *testing.T) {
	forged := &Principal{Kind: PrincipalDevice, Subject: "victim-device", TenantID: deviceTestTenant}
	vis, err := forged.RowVisibility(context.Background())
	require.Error(t, err, "unsealed device principal must not derive a row scope")
	assert.Equal(t, tenant.RowVisibility{}, vis)
}

// --- chain: jwtClaimsToPrincipal dispatch (user vs device) ---

func TestJWTClaimsToPrincipal_DeviceBranch(t *testing.T) {
	dev, err := jwtClaimsToPrincipal(Claims{
		Subject:       "device-7",
		TenantID:      deviceTestTenant,
		PrincipalKind: PrincipalKindClaimDevice,
	})
	require.NoError(t, err)
	assert.Equal(t, PrincipalDevice, dev.Kind)

	usr, err := jwtClaimsToPrincipal(Claims{Subject: "user-7"})
	require.NoError(t, err)
	assert.Equal(t, PrincipalUser, usr.Kind, "absent principal_kind = user (default)")
}

// --- end-to-end: Issue device token → VerifyIntent → AuthenticateBearer ---

func TestDeviceToken_EndToEnd(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	tok, err := issuer.Issue(TokenIntentAccess, "device-42", IssueOptions{
		PrincipalKind: PrincipalKindClaimDevice,
		TenantID:      deviceTestTenant,
		Audience:      []string{"gocell"},
	})
	require.NoError(t, err)

	ctx, p, err := AuthenticateBearer(context.Background(), verifier, tok)
	require.NoError(t, err)
	assert.Equal(t, PrincipalDevice, p.Kind)
	assert.Equal(t, "device-42", p.Subject)
	assert.Equal(t, deviceTestTenant, p.TenantID)

	vis, err := p.RowVisibility(ctx)
	require.NoError(t, err)
	assert.Equal(t, tenant.RowScopeDevice, vis.Scope())
	assert.Equal(t, "device-42", vis.Subject())

	// principal ctxkeys propagate device identity across the async boundary.
	gotSubject, ok := ctxkeys.SubjectIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "device-42", gotSubject)
	gotTenant, ok := ctxkeys.TenantIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, deviceTestTenant, gotTenant)
}

func TestDeviceToken_UnknownPrincipalKind_FailClosed(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// Issuer trusts its caller (like TenantID); the verifier is the fail-closed
	// gate that rejects an unknown principal_kind so it can never silently
	// fall through to a user mint.
	tok, err := issuer.Issue(TokenIntentAccess, "x", IssueOptions{
		PrincipalKind: PrincipalKindClaim("bogus"),
		TenantID:      deviceTestTenant,
		Audience:      []string{"gocell"},
	})
	require.NoError(t, err)

	_, _, err = AuthenticateBearer(context.Background(), verifier, tok)
	require.Error(t, err, "unknown principal_kind must be rejected at verify")
}

// TestPrincipalKind_NonStringClaim_FailClosed proves a present-but-non-string
// principal_kind claim is rejected fail-closed at verify, NOT silently treated
// as absent (which would downgrade a malformed signed marker to the user
// default and bypass the device boundary). Regression for pr-review F2.
func TestPrincipalKind_NonStringClaim_FailClosed(t *testing.T) {
	ks := mustTestKeySet(t)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// A signed token whose principal_kind claim is a number, not a string.
	raw := jwt.MapClaims{
		"sub":            "u1",
		"iss":            "gocell",
		"aud":            "gocell",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
		"token_use":      string(TokenIntentAccess),
		"principal_kind": 123, // present but NOT a string
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, raw)
	tok.Header["kid"] = ks.SigningKeyID()
	tok.Header["typ"] = TypHeaderForIntent(TokenIntentAccess)
	tokenStr, err := tok.SignedString(ks.SigningKey())
	require.NoError(t, err)

	_, err = verifier.VerifyIntent(context.Background(), tokenStr, TokenIntentAccess)
	require.Error(t, err, "present-but-non-string principal_kind must fail closed, not become user default")
}

// --- super-admin: production chain mints RowScopeAll + mandatory audit ---

func TestSuperAdmin_EndToEnd_RowScopeAll_Audit(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	capture := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tok, err := issuer.Issue(TokenIntentAccess, "super-alice", IssueOptions{
		Roles:    []string{RoleSuperAdmin},
		TenantID: deviceTestTenant,
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	ctx, p, err := AuthenticateBearer(context.Background(), verifier, tok)
	require.NoError(t, err)
	require.Equal(t, PrincipalUser, p.Kind)

	vis, err := p.RowVisibility(ctx)
	require.NoError(t, err)
	assert.Equal(t, tenant.RowScopeAll, vis.Scope())
	require.Equal(t, 1, countMandatoryAuditRecords(capture.records),
		"super-admin production chain must emit the FR-007 mandatory audit (sessionmint.MintAccess is the production issuer)")

	// Strengthen: assert the captured slog.Error record carries correct values,
	// not just the presence of keys (FR-007 attr value contract).
	assertAuditAttrValues(t, capture.records, "super-alice", "all", "cross_tenant_read")
}

// assertAuditAttrValues finds the first slog.Error record that matches
// hasMandatoryAuditAttrs and asserts the values of actor, scope, and reason.
// Relies on the captureHandler defined in rowscope_test.go.
func assertAuditAttrValues(t *testing.T, records []slog.Record, wantActor, wantScope, wantReason string) {
	t.Helper()
	for _, r := range records {
		if r.Level != slog.LevelError {
			continue
		}
		var actor, scope, reason string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "actor":
				actor = a.Value.String()
			case "scope":
				scope = a.Value.String()
			case "reason":
				reason = a.Value.String()
			}
			return true
		})
		if actor == "" && scope == "" && reason == "" {
			continue // not the mandatory audit record
		}
		assert.Equal(t, wantActor, actor, "audit attr 'actor' value")
		assert.Equal(t, wantScope, scope, "audit attr 'scope' value")
		assert.Equal(t, wantReason, reason, "audit attr 'reason' value")
		return
	}
	t.Errorf("no slog.Error audit record found to assert attr values")
}

// TestRoleScopes_EndToEnd_NoAccidentalEscalation proves ordinary admin/user
// tokens minted through the production chain cannot accidentally derive the
// privileged RowScopeAll/RowScopeDevice obligations.
func TestRoleScopes_EndToEnd_NoAccidentalEscalation(t *testing.T) {
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "gocell", time.Hour, clock.Real())
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, clock.Real(), WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	cases := []struct {
		name      string
		roles     []string
		wantScope tenant.RowScope
	}{
		{name: "admin_tenant_scope", roles: []string{RoleAdmin}, wantScope: tenant.RowScopeTenant},
		{name: "plain_user_self_scope", roles: nil, wantScope: tenant.RowScopeSelf},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := issuer.Issue(TokenIntentAccess, "subj-"+tc.name, IssueOptions{
				Roles:    tc.roles,
				TenantID: deviceTestTenant,
				Audience: []string{"gocell"},
			})
			require.NoError(t, err)
			ctx, p, err := AuthenticateBearer(context.Background(), verifier, tok)
			require.NoError(t, err)
			vis, err := p.RowVisibility(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.wantScope, vis.Scope())
			assert.NotEqual(t, tenant.RowScopeAll, vis.Scope())
			assert.NotEqual(t, tenant.RowScopeDevice, vis.Scope())
		})
	}
}
