package authorizationdecide

// authorize_as_test.go — tests for Service.AuthorizeAs (explicit-subject
// authorization path, #1904) and the subjectSource interface refactoring.
//
// These tests verify:
//   - AuthorizeAs with no ctx principal still decides (decoupled from ctx).
//   - Device descriptor with sub==resource → Allow (device-self baseline rule).
//   - Device descriptor with sub!=resource → Deny (default-deny).
//   - Invalid/empty tenant in descriptor → fail-closed error (KindPermissionDenied).
//   - principalSubjectSource reproduces all resolveSubject branches unchanged.
//   - descriptorSubjectSource returns the correct values (kind, sub, tenant; nil roles; no claims).

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

const (
	testDeviceID  = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	otherDeviceID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
)

// buildServiceForAuthorizeAs builds a Service with an empty policy store (only
// baseline applies). Uses the shared testTenantIDStr and fixedClockTime.
func buildServiceForAuthorizeAs(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(
		clockmock.New(fixedClockTime),
		mem.NewPolicyRepository(),
		mem.NewResourceAttributeProvider(),
		slog.Default(),
		WithTxManager(outbox.DemoCellTxManager()),
	)
	require.NoError(t, err)
	return svc
}

// TestAuthorizeAs_CompileTimeAssertion verifies the compile-time interface
// assertion var _ auth.SubjectAuthorizer = (*Service)(nil) passes (if the
// implementation is missing, this test file won't compile).
func TestAuthorizeAs_CompileTimeAssertion(t *testing.T) {
	// The assertion is a package-level var in service.go; this test exists to
	// document its purpose and ensure the package-level var stays in scope.
	var _ auth.SubjectAuthorizer = (*Service)(nil)
}

// TestAuthorizeAs_NoPrincipalInCtx_StillDecides verifies that AuthorizeAs
// does NOT require a Principal in ctx. The explicit subject descriptor is the
// sole source of subject attributes.
func TestAuthorizeAs_NoPrincipalInCtx_StillDecides(t *testing.T) {
	svc := buildServiceForAuthorizeAs(t)

	desc, err := auth.NewDeviceSubjectDescriptor(testTenantIDStr, testDeviceID)
	require.NoError(t, err)

	// Empty context — no tenant, no principal.
	ctx := context.Background()
	dec, err := svc.AuthorizeAs(ctx, desc, testDeviceID, authz.PermDeviceEnroll().String())
	// err may or may not be nil — the important thing is it does NOT fail with
	// "no authenticated principal". If err is nil, check the Allow verdict below.
	// If there IS an error it must not be KindPermissionDenied "no authenticated principal".
	if err != nil {
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.NotEqual(t, "authorization-decide: no authenticated principal", ecErr.Message,
			"AuthorizeAs must not fail with 'no authenticated principal'")
		return
	}
	// Allow: device-self rule fires (sub == resource).
	assert.True(t, dec.IsAllow(), "device descriptor with sub==resource must be allowed by device-self baseline")
}

// TestAuthorizeAs_DeviceSelf_Allow verifies that a device descriptor whose
// sub matches the resource triggers the device-self baseline rule (Allow).
func TestAuthorizeAs_DeviceSelf_Allow(t *testing.T) {
	svc := buildServiceForAuthorizeAs(t)

	desc, err := auth.NewDeviceSubjectDescriptor(testTenantIDStr, testDeviceID)
	require.NoError(t, err)

	// No principal in ctx; no tenant in ctx — AuthorizeAs derives tenant from descriptor.
	dec, err := svc.AuthorizeAs(context.Background(), desc, testDeviceID, authz.PermDeviceEnroll().String())
	require.NoError(t, err)
	assert.True(t, dec.IsAllow(),
		"device descriptor with sub==resource must Allow via device-self baseline")
}

// TestAuthorizeAs_DeviceSelf_DifferentResource_Deny verifies that a device
// descriptor whose sub does NOT match the resource is denied (default-deny).
func TestAuthorizeAs_DeviceSelf_DifferentResource_Deny(t *testing.T) {
	svc := buildServiceForAuthorizeAs(t)

	desc, err := auth.NewDeviceSubjectDescriptor(testTenantIDStr, testDeviceID)
	require.NoError(t, err)

	dec, err := svc.AuthorizeAs(context.Background(), desc, otherDeviceID, authz.PermDeviceEnroll().String())
	require.NoError(t, err)
	assert.False(t, dec.IsAllow(),
		"device descriptor with sub!=resource must Deny (default-deny: no matching baseline rule)")
}

// TestAuthorizeAs_InvalidTenant_FailsClosed verifies that an invalid (zero)
// SubjectDescriptor causes AuthorizeAs to fail-closed.
func TestAuthorizeAs_InvalidTenant_FailsClosed(t *testing.T) {
	svc := buildServiceForAuthorizeAs(t)

	// Zero SubjectDescriptor has empty tenant — ParseTenantID will fail.
	var zero auth.SubjectDescriptor
	dec, err := svc.AuthorizeAs(context.Background(), zero, testDeviceID, authz.PermDeviceEnroll().String())
	require.Error(t, err, "zero SubjectDescriptor must fail-closed with an error")
	assert.False(t, dec.IsAllow(), "fail-closed: non-Allow on error")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindPermissionDenied, ecErr.Kind,
		"invalid/empty tenant must fail with KindPermissionDenied (403)")
}

// TestAuthorizeAs_AdminDescriptor_NotConstructable documents the Hard security
// property: no admin SubjectDescriptor can be minted. The only exported constructor
// is NewDeviceSubjectDescriptor; there is no NewAdminSubjectDescriptor.
// This test verifies the contract by ensuring the only available kinds are "device"
// (from NewDeviceSubjectDescriptor) or "" (zero value, invalid).
func TestAuthorizeAs_AdminDescriptor_NotConstructable(t *testing.T) {
	// Only one public constructor; it always produces kind == "device".
	desc, err := auth.NewDeviceSubjectDescriptor(testTenantIDStr, testDeviceID)
	require.NoError(t, err)
	assert.Equal(t, auth.PrincipalDevice.String(), desc.Kind(),
		"NewDeviceSubjectDescriptor must produce kind == device")
	// If we want to assert there is no NewAdminSubjectDescriptor at the call site level,
	// we rely on compilation: there simply is no such symbol. This test documents the intent.
	assert.NotEqual(t, auth.RoleAdmin, desc.Kind(),
		"a device descriptor's Kind must not be 'admin'")
}

// ----- subjectSource interface unit tests -----

// TestPrincipalSubjectSource_Sub verifies the principalSubjectSource reproduces
// the resolveSubject "sub"/"subject" path (including UUID canonicalization).
func TestPrincipalSubjectSource_Sub(t *testing.T) {
	const ownerID = "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"
	const wantID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: ownerID, TenantID: testTenantIDStr}
	src := principalSubjectSource{p: p}
	vals, found := src.sub()
	assert.True(t, found)
	assert.Equal(t, wantID, vals, "principalSubjectSource.sub must canonicalize UUID")
}

// TestPrincipalSubjectSource_EmptySub verifies that an empty Subject yields not-found.
func TestPrincipalSubjectSource_EmptySub(t *testing.T) {
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "", TenantID: testTenantIDStr}
	src := principalSubjectSource{p: p}
	_, found := src.sub()
	assert.False(t, found, "empty Subject must be not-found (fail-closed)")
}

// TestPrincipalSubjectSource_Kind verifies the "kind" path.
func TestPrincipalSubjectSource_Kind(t *testing.T) {
	p := &auth.Principal{Kind: auth.PrincipalDevice, Subject: "dev-1", TenantID: testTenantIDStr}
	src := principalSubjectSource{p: p}
	v, found := src.kind()
	assert.True(t, found)
	assert.Equal(t, auth.PrincipalDevice.String(), v)
}

// TestPrincipalSubjectSource_Roles verifies the roles path (always found, possibly empty).
func TestPrincipalSubjectSource_Roles(t *testing.T) {
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr, Roles: []string{"admin", "viewer"}}
	src := principalSubjectSource{p: p}
	roles := src.roles()
	assert.ElementsMatch(t, []string{"admin", "viewer"}, roles)
}

// TestPrincipalSubjectSource_Tenant verifies the tenant path.
func TestPrincipalSubjectSource_Tenant(t *testing.T) {
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr}
	src := principalSubjectSource{p: p}
	v, found := src.tenant()
	assert.True(t, found)
	assert.Equal(t, testTenantIDStr, v)
}

// TestPrincipalSubjectSource_EmptyTenant_NotFound verifies empty TenantID → not-found.
func TestPrincipalSubjectSource_EmptyTenant_NotFound(t *testing.T) {
	p := &auth.Principal{Kind: auth.PrincipalUser, Subject: "u", TenantID: ""}
	src := principalSubjectSource{p: p}
	_, found := src.tenant()
	assert.False(t, found, "empty TenantID must be not-found")
}

// TestPrincipalSubjectSource_Claim verifies the claims map path.
func TestPrincipalSubjectSource_Claim(t *testing.T) {
	p := &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr,
		Claims: map[string]string{"device_trust": "managed"},
	}
	src := principalSubjectSource{p: p}
	v, found := src.claim("device_trust")
	assert.True(t, found)
	assert.Equal(t, "managed", v)

	_, found = src.claim("missing_key")
	assert.False(t, found)
}

// TestDescriptorSubjectSource_Values verifies the descriptorSubjectSource
// returns the correct Kind, Sub, Tenant values and fails-closed for roles/claims.
func TestDescriptorSubjectSource_Values(t *testing.T) {
	desc, err := auth.NewDeviceSubjectDescriptor(testTenantIDStr, testDeviceID)
	require.NoError(t, err)

	src := descriptorSubjectSource{d: desc}

	// sub
	subVal, found := src.sub()
	assert.True(t, found)
	assert.Equal(t, testDeviceID, subVal)

	// kind
	kindVal, found := src.kind()
	assert.True(t, found)
	assert.Equal(t, auth.PrincipalDevice.String(), kindVal)

	// tenant
	tenantVal, found := src.tenant()
	assert.True(t, found)
	assert.Equal(t, testTenantIDStr, tenantVal)

	// roles → nil (descriptors carry no roles)
	roles := src.roles()
	assert.Nil(t, roles, "descriptorSubjectSource.roles() must return nil")

	// claim → not-found
	_, found = src.claim("any_key")
	assert.False(t, found, "descriptorSubjectSource.claim must always return not-found")
}

// TestAuthorizeAs_BehaviorPreservation_AuthorizeUnchanged verifies that the
// existing Authorize path continues to work exactly as before: a user ctx
// principal + matching resource → Allow via the user-read self rule. The
// principalSubjectSource refactoring must not change any existing behavior.
func TestAuthorizeAs_BehaviorPreservation_AuthorizeUnchanged(t *testing.T) {
	const ownerID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	eng := memEngineWithPolicies(t)
	ownerPrincipal := &auth.Principal{
		Kind:     auth.PrincipalUser,
		Subject:  ownerID,
		TenantID: testTenantIDStr,
		Roles:    []string{},
	}
	dec, err := eng.Authorize(reqCtx(ownerPrincipal), ownerID, ownerID, authz.PermUserRead().String())
	require.NoError(t, err)
	assert.True(t, dec.IsAllow(), "Authorize behavior must be preserved after subjectSource refactoring")
}
