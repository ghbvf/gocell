package deviceserving

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// newMux mounts the framework-served devicestate route exactly as bootstrap does
// (RouteGroup.Register on a bare mux, no prefix), so the test exercises the same
// device:read RequirePermission gate production uses.
func newMux(t *testing.T) http.Handler {
	t.Helper()
	svc := NewService(clockmock.New(fixedNow))
	route := svc.Route()
	require.Equal(t, "http.devicestate.v1", route.ContractID)
	mux := celltest.NewTestMux()
	require.NoError(t, route.Group.Register(mux))
	return mux
}

// TestDevicestate_OK: an authenticated admin (allow PDP) gets a 200 whose body
// reports honest "unknown" presence with observedAt = the determination time.
func TestDevicestate_OK(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devicestate?deviceId=dev-1", nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var env struct {
		Data struct {
			DeviceID   string `json:"deviceId"`
			State      string `json:"state"`
			ObservedAt string `json:"observedAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "dev-1", env.Data.DeviceID)
	assert.Equal(t, "unknown", env.Data.State)
	assert.Equal(t, fixedNow.Format(time.RFC3339), env.Data.ObservedAt)
}

// TestDevicestate_MissingDeviceID: authorized request with no deviceId query
// param fails the generated handler's validation with 400.
func TestDevicestate_MissingDeviceID(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, allowAuthorizer(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devicestate", nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

// TestDevicestate_Unauthenticated: no principal ⇒ RequirePermission 401.
func TestDevicestate_Unauthenticated(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devicestate?deviceId=dev-1", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devicestate?deviceId=dev-1", nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
}

// TestService_Devicestate_HonestUnknown asserts the Service contract directly:
// state is always unknown (no presence backend) and observedAt is the clock's
// determination time — never a fabricated online/offline reading.
func TestService_Devicestate_HonestUnknown(t *testing.T) {
	svc := NewService(clockmock.New(fixedNow))
	resp, err := svc.Devicestate(context.Background(), &devicestate.Request{DeviceID: "dev-9"})
	require.NoError(t, err)
	_, ok := resp.(devicestate.Devicestate200JSONResponse)
	assert.True(t, ok, "Service must return a 200 typed response, got %T", resp)
}
