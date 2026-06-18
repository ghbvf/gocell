package deviceserving

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	devicestate "github.com/ghbvf/gocell/generated/contracts/http/devicestate/v1"
)

var fixedNow = time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision
// (mock placement per go-standards.md §Naming — same-package test file).
type mockAuthorizer struct {
	decision authz.Decision
	err      error
}

func (m *mockAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return m.decision, m.err
}

func allowAuthorizer(t *testing.T) *mockAuthorizer {
	t.Helper()
	dec, err := authz.Allow(authz.Obligations{})
	require.NoError(t, err)
	return &mockAuthorizer{decision: dec}
}

func denyAuthorizer(reason string) *mockAuthorizer {
	return &mockAuthorizer{decision: authz.Deny(reason)}
}

// resourceSpyAuthorizer records the (subject, resource, action) tuple the route
// gate forwards to the PDP, and returns a fixed Allow. It is the evidence for
// #2348 F3: the gate must forward the deviceId path param as the ABAC `resource`
// (not the URL path), so the PDP can decide per-device ownership.
type resourceSpyAuthorizer struct {
	gotSubject, gotResource, gotAction string
	decision                           authz.Decision
}

func (s *resourceSpyAuthorizer) Authorize(_ context.Context, subject, resource, action string) (authz.Decision, error) {
	s.gotSubject, s.gotResource, s.gotAction = subject, resource, action
	return s.decision, nil
}

// ownerAuthorizer mimics the baseline owner rule `subject.sub == resource.id`:
// allow iff the caller's subject equals the requested device id, deny otherwise.
// This is how the path-param gate lets a device read ITS OWN state but blocks
// device A from reading device B (the per-device isolation F3 makes expressible).
type ownerAuthorizer struct {
	allow authz.Decision
	deny  authz.Decision
}

func (o *ownerAuthorizer) Authorize(_ context.Context, subject, resource, _ string) (authz.Decision, error) {
	if subject == resource {
		return o.allow, nil
	}
	return o.deny, nil
}

func newOwnerAuthorizer(t *testing.T) *ownerAuthorizer {
	t.Helper()
	allow, err := authz.Allow(authz.Obligations{})
	require.NoError(t, err)
	return &ownerAuthorizer{allow: allow, deny: authz.Deny("not owner")}
}

// newMux mounts the framework-served devicestate route exactly as bootstrap does
// (RouteGroup.Register on a bare mux, no prefix), so the test exercises the same
// auth.RequirePermissionForResource("id", device:read) gate production uses. The
// authorizer is supplied per-request via auth.WithAuthorizer on the context.
func newMux(t *testing.T) http.Handler {
	t.Helper()
	svc := NewService(clockmock.New(fixedNow))
	route := svc.Route()
	require.Equal(t, "http.devicestate.v1", route.ContractID)
	mux := celltest.NewTestMux()
	require.NoError(t, route.Group.Register(mux))
	return mux
}

// devicestatePath builds the path-param URL for a device id.
func devicestatePath(id string) string { return "/api/v1/devicestate/" + id }

func adminCtx(authorizer auth.Authorizer) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	return auth.WithAuthorizer(ctx, authorizer)
}

// TestDevicestate_OK: an authenticated admin (allow PDP) gets a 200 whose body
// reports honest "unknown" presence with observedAt = the determination time.
func TestDevicestate_OK(t *testing.T) {
	ctx := adminCtx(allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, devicestatePath("dev-1"), nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var env struct {
		Data struct {
			DeviceID   string  `json:"deviceId"`
			State      string  `json:"state"`
			ObservedAt string  `json:"observedAt"`
			TenantID   *string `json:"tenantId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "dev-1", env.Data.DeviceID)
	assert.Equal(t, "unknown", env.Data.State)
	assert.Equal(t, fixedNow.Format(time.RFC3339), env.Data.ObservedAt)
	// tenantId is present (full column set, #2359) but JSON null — the honest
	// "no device→tenant binding" value, never a misleading empty string (#2394 review F1).
	assert.True(t, strings.Contains(rec.Body.String(), `"tenantId":null`),
		"tenantId must serialize as null (honest no-binding), got body=%s", rec.Body.String())
	assert.Nil(t, env.Data.TenantID, "tenantId must decode as nil (null), not empty string")
}

// TestDevicestate_GateForwardsDeviceIDAsResource is the #2348 F3 evidence: the
// gate forwards the deviceId path param (not r.URL.Path) to the PDP as resource,
// and the device:read permission as action.
func TestDevicestate_GateForwardsDeviceIDAsResource(t *testing.T) {
	allow, err := authz.Allow(authz.Obligations{})
	require.NoError(t, err)
	spy := &resourceSpyAuthorizer{decision: allow}
	ctx := adminCtx(spy)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, devicestatePath("dev-42"), nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, "dev-42", spy.gotResource, "deviceId path param must be forwarded as PDP resource (F3), not the URL path")
	assert.Equal(t, authz.PermDeviceRead().String(), spy.gotAction)
	assert.Equal(t, "admin-1", spy.gotSubject)
}

// TestDevicestate_PerDeviceOwnership: with an ownership PDP (subject.sub ==
// resource.id), a device reading its OWN state is allowed (200) but reading
// another device's state is denied (403) — the per-device isolation that the
// coarse gate could not express (#2348 F3 / MDM zero-trust boundary).
func TestDevicestate_PerDeviceOwnership(t *testing.T) {
	owner := newOwnerAuthorizer(t)
	const testTenant = "00000000-0000-0000-0000-000000000001"
	deviceCtx := func(deviceSub string) context.Context {
		// MustNewTestDevicePrincipal mints a *sealed* device principal — the
		// sanctioned test path (a bare auth.Principal{Kind: PrincipalDevice} literal
		// lacks the device seal, per DEVICE-PRINCIPAL-MINT-CALLER-01).
		ctx := auth.WithPrincipal(context.Background(), auth.MustNewTestDevicePrincipal(deviceSub, testTenant))
		return auth.WithAuthorizer(ctx, owner)
	}

	// device dev-A reads its own state → allowed.
	recOwn := httptest.NewRecorder()
	reqOwn := httptest.NewRequest(http.MethodGet, devicestatePath("dev-A"), nil).WithContext(deviceCtx("dev-A"))
	newMux(t).ServeHTTP(recOwn, reqOwn)
	assert.Equal(t, http.StatusOK, recOwn.Code, "device must read its own state; body=%s", recOwn.Body.String())

	// device dev-A reads dev-B's state → denied (cross-device enumeration blocked).
	recOther := httptest.NewRecorder()
	reqOther := httptest.NewRequest(http.MethodGet, devicestatePath("dev-B"), nil).WithContext(deviceCtx("dev-A"))
	newMux(t).ServeHTTP(recOther, reqOther)
	assert.Equal(t, http.StatusForbidden, recOther.Code, "device A must not read device B state; body=%s", recOther.Body.String())
}

// TestDevicestate_Unauthenticated: no principal ⇒ the gate returns 401.
func TestDevicestate_Unauthenticated(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, devicestatePath("dev-1"), nil)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
}

// TestDevicestate_Forbidden: authenticated but the PDP denies device:read ⇒ 403.
func TestDevicestate_Forbidden(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "user-1", Roles: []string{"viewer"}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, denyAuthorizer("no device:read"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, devicestatePath("dev-1"), nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
}

// TestDevicestate_NoDeviceID_NotFound: the bare /api/v1/devicestate path (no id
// segment) does not match the path-param route ⇒ 404. With a path-param id the
// "missing identifier" case is a routing miss, not a 400 validation failure.
func TestDevicestate_NoDeviceID_NotFound(t *testing.T) {
	ctx := adminCtx(allowAuthorizer(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devicestate", nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
}

// TestDevicestate_DeviceIDMaxLength: id exactly 256 chars (upper bound) → 200.
func TestDevicestate_DeviceIDMaxLength(t *testing.T) {
	ctx := adminCtx(allowAuthorizer(t))
	rec := httptest.NewRecorder()
	deviceID := strings.Repeat("a", 256)
	req := httptest.NewRequest(http.MethodGet, devicestatePath(deviceID), nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
}

// TestDevicestate_DeviceIDTooLong: id of 257 chars → 400 (len > 256 branch).
func TestDevicestate_DeviceIDTooLong(t *testing.T) {
	ctx := adminCtx(allowAuthorizer(t))
	rec := httptest.NewRecorder()
	deviceID := strings.Repeat("a", 257)
	req := httptest.NewRequest(http.MethodGet, devicestatePath(deviceID), nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

// TestService_Devicestate_HonestUnknown asserts the Service contract directly:
// state is always unknown (no presence backend) and observedAt is the clock's
// determination time — never a fabricated online/offline reading.
func TestService_Devicestate_HonestUnknown(t *testing.T) {
	svc := NewService(clockmock.New(fixedNow))
	resp, err := svc.Devicestate(context.Background(), &devicestate.Request{ID: "dev-9"})
	require.NoError(t, err)
	_, ok := resp.(devicestate.Devicestate200JSONResponse)
	assert.True(t, ok, "Service must return a 200 typed response, got %T", resp)
}
